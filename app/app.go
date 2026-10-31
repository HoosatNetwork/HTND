package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/prefixmanager"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/config"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/os/execenv"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/os/limits"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/os/signal"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/os/winservice"
	"github.com/HoosatNetwork/HTND/v2/util/panics"
	"github.com/HoosatNetwork/HTND/v2/util/profiling"
	"github.com/HoosatNetwork/HTND/v2/version"
)

const (
	leveldbCacheSizeMiB  = 256
	pebbledbCacheSizeMiB = 2048
	defaultDataDirname   = "datadir2"
)

var desiredLimits = &limits.DesiredLimits{
	FileLimitWant: 2048,
	FileLimitMin:  1024,
}

var serviceDescription = &winservice.ServiceDescription{
	Name:        "htndsvc",
	DisplayName: "htnd Service",
	Description: "Downloads and stays synchronized with the Hoosat blockDAG and " +
		"provides DAG services to applications.",
}

type htndApp struct {
	cfg *config.Config
}

// StartApp starts the htnd app, and blocks until it finishes running
func StartApp() error {
	return StartAppWithConfig(nil)
}

// StartAppWithConfig starts the htnd app with a pre-loaded config, and blocks until it finishes running
// If cfg is nil, it will load the config internally
func StartAppWithConfig(cfg *config.Config) error {
	execenv.Initialize(desiredLimits)

	// Load configuration and parse command line. This function also
	// initializes logging and configures it accordingly.
	var err error
	if cfg == nil {
		cfg, err = config.LoadConfig()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return err
		}
	}
	defer logger.BackendLog.Close()
	defer panics.HandlePanic(log, "MAIN", nil)

	app := &htndApp{cfg: cfg}
	// Call serviceMain on Windows to handle running as a service. When
	// the return isService flag is true, exit now since we ran as a
	// service. Otherwise, just fall through to normal operation.
	if runtime.GOOS == "windows" {
		isService, err := winservice.WinServiceMain(app.main, serviceDescription, cfg)
		if err != nil {
			return err
		}
		if isService {
			return nil
		}
	}

	return app.main(nil)
}

func (app *htndApp) main(startedChan chan<- struct{}) (resultErr error) {
	// Get a channel that will be closed when a shutdown signal has been
	// triggered either from an OS signal such as SIGINT (Ctrl+C) or from
	// another subsystem such as the RPC server.
	interrupt := signal.InterruptListener()
	defer log.Info("Shutdown complete")

	// Show version at startup.
	log.Infof("Version %s", version.Version())

	// Enable http profiling server if requested.
	if !app.cfg.PrepareDowngrade && app.cfg.Profile != "" {
		profiling.Start(app.cfg.Profile, log)
	}
	profiling.TrackHeap(app.cfg.AppDir, log)

	// Return now if an interrupt signal was triggered.
	if signal.InterruptRequested(interrupt) {
		if app.cfg.PrepareDowngrade {
			return context.Canceled
		}
		return nil
	}

	if app.cfg.ResetDatabase {
		err := removeDatabase(app.cfg)
		if err != nil {
			log.Error(err)
			return err
		}
	}

	// Open the database
	databaseContext, err := openDB(app.cfg)
	if err != nil {
		log.Errorf("Loading database failed: %+v", err)
		return err
	}

	defer func() {
		log.Infof("Gracefully shutting down the database...")
		err := databaseContext.Close()
		if err != nil {
			log.Errorf("Failed to close the database: %s", err)
			if app.cfg.PrepareDowngrade {
				resultErr = errors.Join(resultErr, err)
			}
		}
	}()

	// Return now if an interrupt signal was triggered.
	if signal.InterruptRequested(interrupt) {
		if app.cfg.PrepareDowngrade {
			return context.Canceled
		}
		return nil
	}

	if app.cfg.PrepareDowngrade {
		return prepareDowngrade(app.cfg, databaseContext, interrupt)
	}

	// Create componentManager and start it.
	componentManager, err := NewComponentManager(app.cfg, databaseContext, interrupt)
	if err != nil {
		log.Errorf("Unable to start htnd: %+v", err)
		return err
	}

	const shutdownTimeout = 2 * time.Minute
	defer stopComponents(componentManager.Stop, shutdownTimeout)

	componentManager.Start()

	if startedChan != nil {
		startedChan <- struct{}{}
	}

	// Wait until the interrupt signal is received from an OS signal or
	// shutdown is requested through one of the subsystems such as the RPC
	// server.
	<-interrupt
	return nil
}

// prepareDowngrade runs --prepare-downgrade: consensus.PrepareDowngrade on the active
// consensus with the node's own configuration, and nothing else. An interrupt stops it
// resumably; running the flag again continues.
func prepareDowngrade(cfg *config.Config, db database.Database, interrupt <-chan struct{}) error {
	consensusConfig, err := newConsensusConfig(cfg)
	if err != nil {
		return err
	}
	activePrefix, exists, err := prefixmanager.ActivePrefix(db)
	if err != nil {
		return err
	}
	if !exists {
		log.Infof("Downgrade preparation: the database has no consensus data; nothing to do")
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-interrupt:
			cancel()
		case <-ctx.Done():
		}
	}()
	report, err := consensus.PrepareDowngrade(ctx, consensusConfig, db, activePrefix)
	if err != nil {
		log.Errorf("Downgrade preparation failed; run --prepare-downgrade again with this release: %+v", err)
		return err
	}
	log.Infof("Downgrade preparation done: %s", report)
	return nil
}

// stopComponents runs stop and returns once it has finished, so that the database Close deferred before it runs
// after it. If stop does not finish within timeout, or reports that something is still running, goroutines may still
// be using the database: closing it under them makes them fail with "pebble: closed" and exit the process in the
// middle of the close. The process exits here instead, without closing the database. For the database that is the
// same as a crash, which its write-ahead log recovers from on the next start when the WAL is enabled (the default).
func stopComponents(stop func() bool, timeout time.Duration) {
	log.Infof("Gracefully shutting down htnd...")

	stopped := make(chan bool, 1)
	go func() {
		stopped <- stop()
	}()

	select {
	case ok := <-stopped:
		if !ok {
			panics.Exit(log, "htnd did not shut down cleanly; not closing the database")
		}
	case <-time.After(timeout):
		panics.Exit(log, fmt.Sprintf("graceful shutdown timed out after %s; not closing the database", timeout))
	}
	log.Infof("htnd shutdown complete")
}

// dbPath returns the path to the block database given a database type.
func databasePath(cfg *config.Config) string {
	return filepath.Join(cfg.AppDir, defaultDataDirname)
}

func removeDatabase(cfg *config.Config) error {
	dbPath := databasePath(cfg)
	return os.RemoveAll(dbPath)
}

func openDB(cfg *config.Config) (database.Database, error) {
	dbPath := databasePath(cfg)

	err := checkDatabaseVersion(dbPath)
	if err != nil {
		return nil, err
	}

	if strings.EqualFold(cfg.DbType, "leveldb") {
		log.Infof("Loading LevelDB database  from '%s'", dbPath)
		db, err := ldb.NewLevelDB(dbPath, leveldbCacheSizeMiB)
		if err != nil {
			return nil, err
		}
		return db, nil
	}
	// For pebble and all other db types, use PebbleDB
	log.Infof("Loading %s database from '%s'", cfg.DbType, dbPath)
	db, err := pebble.NewPebbleDB(dbPath, pebbledbCacheSizeMiB)
	if err != nil {
		return nil, err
	}
	return db, nil
}
