package headersselectedtipstore

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus/database"
	"github.com/Hoosat-Oy/HTND/domain/consensus/database/serialization"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/util/staging"
	"github.com/cockroachdb/errors"
)

var keyName = []byte("headers-selected-tip")

type headerSelectedTipStore struct {
	shardID model.StagingShardID
	cache   *externalapi.DomainHash
	key     model.DBKey
}

// New instantiates a new HeaderSelectedTipStore
func New(prefixBucket model.DBBucket) model.HeaderSelectedTipStore {
	return &headerSelectedTipStore{
		shardID: staging.GenerateShardingID(),
		key:     prefixBucket.Key(keyName),
	}
}

func (hsts *headerSelectedTipStore) Has(dbContext model.DBReader, stagingArea *model.StagingArea) (bool, error) {
	stagingShard := hsts.stagingShard(stagingArea)

	if stagingShard.newSelectedTip != nil {
		return true, nil
	}

	if hsts.cache != nil {
		return true, nil
	}

	return dbContext.Has(hsts.key)
}

func (hsts *headerSelectedTipStore) Stage(stagingArea *model.StagingArea, selectedTip *externalapi.DomainHash) {
	stagingShard := hsts.stagingShard(stagingArea)
	stagingShard.newSelectedTip = selectedTip
}

func (hsts *headerSelectedTipStore) IsStaged(stagingArea *model.StagingArea) bool {
	return hsts.stagingShard(stagingArea).isStaged()
}

func (hsts *headerSelectedTipStore) UnstageAll(stagingArea *model.StagingArea) {
	stagingShard := hsts.stagingShard(stagingArea)
	stagingShard.UnstageAll()
}

func (hsts *headerSelectedTipStore) HeadersSelectedTip(dbContext model.DBReader, stagingArea *model.StagingArea) (
	*externalapi.DomainHash, error) {

	stagingShard := hsts.stagingShard(stagingArea)

	if stagingShard.newSelectedTip != nil {
		return stagingShard.newSelectedTip, nil
	}

	if hsts.cache != nil {
		return hsts.cache, nil
	}

	selectedTipBytes, err := dbContext.Get(hsts.key)
	if errors.Is(err, database.ErrNotFound) {
		return nil, errors.Wrapf(err, "Headers selected tip %s does not exist in db", hsts.key)
	}
	if err != nil {
		return nil, err
	}

	selectedTip, err := hsts.deserializeHeadersSelectedTip(selectedTipBytes)
	if err != nil {
		return nil, err
	}
	hsts.cache = selectedTip
	return hsts.cache, nil
}

func (hsts *headerSelectedTipStore) serializeHeadersSelectedTip(selectedTip *externalapi.DomainHash) ([]byte, error) {
	return serialization.DomainHashToDbHash(selectedTip).MarshalVT()
}

func (hsts *headerSelectedTipStore) deserializeHeadersSelectedTip(selectedTipBytes []byte) (*externalapi.DomainHash, error) {
	dbHash := &serialization.DbHash{}
	err := dbHash.UnmarshalVT(selectedTipBytes)
	if err != nil {
		return nil, err
	}

	return serialization.DbHashToDomainHash(dbHash)
}
