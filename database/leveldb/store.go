package leveldb

import (
	"encoding/json"

	"github.com/golang/protobuf/proto"
	"github.com/tendermint/tmlibs/common"
	dbm "github.com/tendermint/tmlibs/db"

	"github.com/bytom/database"
	"github.com/bytom/database/storage"
	"github.com/bytom/errors"
	"github.com/bytom/protocol/bc"
	"github.com/bytom/protocol/bc/types"
	"github.com/bytom/protocol/state"
)

var (
	blockStoreKey     = []byte("blockStore")
	blockPrefix       = []byte("B:")
	blockHeaderPrefix = []byte("BH:")
	blockSeedPrefix   = []byte("BS:")
	txStatusPrefix    = []byte("BTS:")
)

func loadBlockStoreStateJSON(db dbm.DB) database.BlockStoreStateJSON {
	bytes := db.Get(blockStoreKey)
	if bytes == nil {
		return database.BlockStoreStateJSON{
			Height: 0,
		}
	}
	bsj := database.BlockStoreStateJSON{}
	if err := json.Unmarshal(bytes, &bsj); err != nil {
		common.PanicCrisis(common.Fmt("Could not unmarshal bytes: %X", bytes))
	}
	return bsj
}

// A Store encapsulates storage for blockchain validation.
// It satisfies the interface protocol.Store, and provides additional
// methods for querying current data.
type Store struct {
	db    dbm.DB
	cache blockCache
}

func calcBlockKey(hash *bc.Hash) []byte {
	return append(blockPrefix, hash.Bytes()...)
}

func calcBlockHeaderKey(hash *bc.Hash) []byte {
	return append(blockHeaderPrefix, hash.Bytes()...)
}

func calcSeedKey(hash *bc.Hash) []byte {
	return append(blockSeedPrefix, hash.Bytes()...)
}

func calcTxStatusKey(hash *bc.Hash) []byte {
	return append(txStatusPrefix, hash.Bytes()...)
}

// GetBlock return the block by given hash
func GetBlock(db dbm.DB, hash *bc.Hash) *types.Block {
	bytez := db.Get(calcBlockKey(hash))
	if bytez == nil {
		return nil
	}

	block := &types.Block{}
	block.UnmarshalText(bytez)
	return block
}

// NewStore creates and returns a new Store object.
func NewStore(db dbm.DB) *Store {
	cache := newBlockCache(func(hash *bc.Hash) *types.Block {
		return GetBlock(db, hash)
	})
	return &Store{
		db:    db,
		cache: cache,
	}
}

// GetUtxo will search the utxo in db
func (s *Store) GetUtxo(hash *bc.Hash) (*storage.UtxoEntry, error) {
	return getUtxo(s.db, hash)
}

// BlockExist check if the block is stored in disk
func (s *Store) BlockExist(hash *bc.Hash) bool {
	block, err := s.cache.lookup(hash)
	return err == nil && block != nil
}

// GetBlock return the block by given hash
func (s *Store) GetBlock(hash *bc.Hash) (*types.Block, error) {
	return s.cache.lookup(hash)
}

// GetBlockHeader return the block by given hash
func (s *Store) GetBlockHeader(hash *bc.Hash) (*types.BlockHeader, error) {
	bytez := s.db.Get(calcBlockHeaderKey(hash))
	if bytez == nil {
		return nil, errors.New("can't find the block header by given hash")
	}

	bh := &types.BlockHeader{}
	err := bh.UnmarshalText(bytez)
	return bh, err
}

// GetSeed will return the seed of given block
func (s *Store) GetSeed(hash *bc.Hash) (*bc.Hash, error) {
	data := s.db.Get(calcSeedKey(hash))
	if data == nil {
		return nil, errors.New("can't find the seed by given hash")
	}

	seed := &bc.Hash{}
	if err := proto.Unmarshal(data, seed); err != nil {
		return nil, errors.Wrap(err, "unmarshaling seed")
	}
	return seed, nil
}

// GetTransactionsUtxo will return all the utxo that related to the input txs
func (s *Store) GetTransactionsUtxo(view *state.UtxoViewpoint, txs []*bc.Tx) error {
	return getTransactionsUtxo(s.db, view, txs)
}

// GetTransactionStatus will return the utxo that related to the block hash
func (s *Store) GetTransactionStatus(hash *bc.Hash) (*bc.TransactionStatus, error) {
	data := s.db.Get(calcTxStatusKey(hash))
	if data == nil {
		return nil, errors.New("can't find the transaction status by given hash")
	}

	ts := &bc.TransactionStatus{}
	if err := proto.Unmarshal(data, ts); err != nil {
		return nil, errors.Wrap(err, "unmarshaling transaction status")
	}
	return ts, nil
}

// GetStoreStatus return the BlockStoreStateJSON
func (s *Store) GetStoreStatus() database.BlockStoreStateJSON {
	return loadBlockStoreStateJSON(s.db)
}

// GetMainchain read the mainchain map from db
func (s *Store) GetMainchain(hash *bc.Hash) (map[uint64]*bc.Hash, error) {
	return getMainchain(s.db, hash)
}

// SaveBlock persists a new block in the database.
func (s *Store) SaveBlock(block *types.Block, ts *bc.TransactionStatus, seed *bc.Hash) error {
	binaryBlock, err := block.MarshalText()
	if err != nil {
		return errors.Wrap(err, "Marshal block meta")
	}

	binaryBlockHeader, err := block.BlockHeader.MarshalText()
	if err != nil {
		return errors.Wrap(err, "Marshal block header")
	}

	binaryTxStatus, err := proto.Marshal(ts)
	if err != nil {
		return errors.Wrap(err, "marshal block transaction status")
	}

	binarySeed, err := proto.Marshal(seed)
	if err != nil {
		return errors.Wrap(err, "marshal block seed")
	}

	blockHash := block.Hash()
	batch := s.db.NewBatch()
	batch.Set(calcBlockKey(&blockHash), binaryBlock)
	batch.Set(calcBlockHeaderKey(&blockHash), binaryBlockHeader)
	batch.Set(calcTxStatusKey(&blockHash), binaryTxStatus)
	batch.Set(calcSeedKey(&blockHash), binarySeed)
	batch.Write()
	return nil
}

// SaveChainStatus save the core's newest status && delete old status
func (s *Store) SaveChainStatus(block *types.Block, view *state.UtxoViewpoint, m map[uint64]*bc.Hash) error {
	hash := block.Hash()
	batch := s.db.NewBatch()

	if err := saveMainchain(batch, m, &hash); err != nil {
		return err
	}

	if err := saveUtxoView(batch, view); err != nil {
		return err
	}

	bytes, err := json.Marshal(database.BlockStoreStateJSON{Height: block.Height, Hash: &hash})
	if err != nil {
		return err
	}

	batch.Set(blockStoreKey, bytes)
	batch.Write()
	cleanMainchainDB(s.db, &hash)
	return nil
}

func (s *Store)SaveUtxoView(view *state.UtxoViewpoint) error {
	batch := s.db.NewBatch()
	return saveUtxoView(batch, view)
}