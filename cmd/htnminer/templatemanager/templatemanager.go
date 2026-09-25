package templatemanager

import (
	"sync"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/pow"
)

var (
	currentTemplate *externalapi.DomainBlock
	currentState    *pow.State
	isSynced        bool
	lock            = &sync.Mutex{}
)

// Get returns the template to work on
func Get() (*externalapi.DomainBlock, *pow.State, bool) {
	lock.Lock()
	defer lock.Unlock()
	// Shallow copy the block so when the user replaces the header it won't affect the template here.
	if currentTemplate == nil {
		return nil, nil, false
	}
	block := *currentTemplate
	state := *currentState
	return &block, &state, isSynced
}

// Set sets the current template to work on
func Set(template *appmessage.GetBlockTemplateResponseMessage) error {
	block, err := appmessage.RPCBlockToDomainBlock(template.Block, "TEMPLATE_POW_HASH")
	if err != nil {
		return err
	}
	lock.Lock()
	defer lock.Unlock()
	currentTemplate = block
	currentState = pow.NewState(block.Header.ToMutable())
	isSynced = template.IsSynced
	return nil
}
