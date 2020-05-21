package wallet

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/SkycoinProject/skycoin/src/cipher"
	"github.com/SkycoinProject/skycoin/src/cipher/bip44"
	"github.com/SkycoinProject/skycoin/src/util/file"
	"github.com/SkycoinProject/skycoin/src/wallet/crypto"
)

// TransactionsFinder interface for finding address related transaction hashes
type TransactionsFinder interface {
	AddressesActivity(addrs []cipher.Addresser) ([]bool, error)
}

// Service wallet service struct
type Service struct {
	sync.RWMutex
	wallets Wallets
	config  Config
	// fingerprints is used to check for duplicate deterministic wallets
	fingerprints map[string]string
	// registered wallet backends
	loaders  map[string]Loader
	creators map[string]Creator
}

// Loader is the interface that wraps the Load method.
//
// Load loads wallet from specific wallet file path.
type Loader interface {
	Load(data []byte) (Wallet, error)
}

// Creator is the interface that wraps the Create and Type methods.
//
// Create creates a wallet base on the parameters
type Creator interface {
	Create(filename, label, seed string, options Options) (Wallet, error)
}

// Config wallet service config
type Config struct {
	WalletDir       string
	CryptoType      crypto.CryptoType
	EnableWalletAPI bool
	EnableSeedAPI   bool
	Bip44Coin       *bip44.CoinType
	WalletLoaders   map[string]Loader
	WalletCreators  map[string]Creator
}

// NewConfig creates a default Config
func NewConfig() Config {
	bc := bip44.CoinTypeSkycoin
	return Config{
		WalletDir:       "./",
		CryptoType:      crypto.DefaultCryptoType,
		EnableWalletAPI: false,
		EnableSeedAPI:   false,
		Bip44Coin:       &bc,
	}
}

// NewService new wallet service
func NewService(c Config) (*Service, error) {
	serv := &Service{
		config:       c,
		fingerprints: make(map[string]string),
		loaders:      make(map[string]Loader),
		creators:     make(map[string]Creator),
	}

	if !serv.config.EnableWalletAPI {
		return serv, nil
	}

	if err := os.MkdirAll(c.WalletDir, os.FileMode(0700)); err != nil {
		return nil, fmt.Errorf("failed to create wallet directory %s: %v", c.WalletDir, err)
	}

	// Removes .wlt.bak files before loading wallets
	if err := removeBackupFiles(serv.config.WalletDir); err != nil {
		return nil, fmt.Errorf("remove .wlt.bak files in %v failed: %v", serv.config.WalletDir, err)
	}

	// loads the wallet loaders
	for t, l := range c.WalletLoaders {
		serv.loaders[t] = l
	}

	// loads the wallet creators
	for t, ctr := range c.WalletCreators {
		serv.creators[t] = ctr
	}

	// Load all wallets from disk
	w, err := serv.loadWallets()
	if err != nil {
		return nil, fmt.Errorf("failed to load all wallets: %v", err)
	}

	// Abort if there are duplicate wallets (identified by fingerprint) on disk
	if wltID, fp, hasDup := w.containsDuplicate(); hasDup {
		return nil, fmt.Errorf("duplicate wallet found with fingerprint %s in file %q", fp, wltID)
	}

	// Abort if there are empty deterministic wallets on disk
	if wltID, hasEmpty := w.containsEmpty(); hasEmpty {
		return nil, fmt.Errorf("empty wallet file found: %q", wltID)
	}

	serv.setWallets(w)

	fields := logrus.Fields{
		"walletDir": serv.config.WalletDir,
	}
	if serv.config.Bip44Coin != nil {
		fields["bip44Coin"] = *serv.config.Bip44Coin
	}
	logger.WithFields(fields).Debug("wallet.NewService complete")

	return serv, nil
}

// WalletDir returns the configured wallet directory
func (serv *Service) WalletDir() (string, error) {
	serv.Lock()
	defer serv.Unlock()
	if !serv.config.EnableWalletAPI {
		return "", ErrWalletAPIDisabled
	}
	return serv.config.WalletDir, nil
}

//  SetEnableWalletAPI sets whether or not enables the wallet related APIs
func (serv *Service) SetEnableWalletAPI(enable bool) {
	serv.config.EnableWalletAPI = enable
}

func (serv *Service) loadWallets() (Wallets, error) {
	dir := serv.config.WalletDir
	entries, err := ioutil.ReadDir(dir)
	if err != nil {
		logger.WithError(err).WithField("dir", dir).Error("loadWallets: ioutil.ReadDir failed")
		return nil, err
	}

	wallets := Wallets{}
	for _, e := range entries {
		if e.Mode().IsRegular() {
			name := e.Name()
			if !strings.HasSuffix(name, WalletExt) {
				logger.WithField("filename", name).Info("loadWallets: skipping file")
				continue
			}

			fullPath := filepath.Join(serv.config.WalletDir, name)
			w, err := serv.Load(fullPath)
			if err != nil {
				logger.WithError(err).WithField("filename", fullPath).Error("loadWallets: loadWallet failed")
				return nil, err
			}

			if w == nil {
				logger.WithField("filename", fullPath).Warning("wallet loading skipped")
				continue
			}

			logger.WithField("filename", fullPath).Info("loadWallets: loaded wallet")

			wallets[name] = w
		}
	}

	for name, w := range wallets {
		// TODO: do validate when creating wallet
		// if err := w.Validate(); err != nil {
		// 	logger.WithError(err).WithField("name", name).Error("loadWallets: wallet.Validate failed")
		// 	return nil, err
		// }

		if w.Coin() != CoinTypeSkycoin {
			err := fmt.Errorf("LoadWallets only support skycoin wallets, %s is a %s wallet", name, w.Coin())
			logger.WithError(err).WithField("name", name).Error()
			return nil, err
		}
	}

	return wallets, nil
}

// Load loads wallet from the given wallet file, it won't not affect the
// state of the service it self, the loaded wallet will be returned.
func (serv *Service) Load(filename string) (Wallet, error) {
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return nil, fmt.Errorf("wallet %q doesn't exist", filename)
	}

	// Load the wallet meta type field from JSON
	m, err := loadWalletMeta(filename)
	if err != nil {
		return nil, err
	}

	if m.Meta.Type == "" {
		err := errors.New("missing meta.type field")
		logger.WithError(err).WithField("filename", filename)
		return nil, err
	}

	// Depending on the wallet type in the wallet metadata header, load the full wallet data
	l, ok := serv.loaders[m.Meta.Type]
	if !ok {
		//return nil, fmt.Errorf("wallet loader for type of %q not found", m.Meta.Type)
		logger.Errorf("wallet loader for type of %q not found", m.Meta.Type)
		return nil, nil
	}

	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	w, err := l.Load(data)
	if err != nil {
		return nil, err
	}

	w.SetFilename(filepath.Base(filename))
	return w, nil
}

func (serv *Service) updateOptions(opts Options) Options {
	// Apply service-configured default settings for wallet options
	if opts.Encrypt && opts.CryptoType == "" {
		opts.CryptoType = serv.config.CryptoType
	}

	if opts.Type == WalletTypeBip44 && opts.Bip44Coin == nil && serv.config.Bip44Coin != nil {
		c := *serv.config.Bip44Coin
		opts.Bip44Coin = &c
	}

	// generate one default address if options.GenerateN is 0
	if opts.GenerateN == 0 {
		opts.GenerateN = 1
	}
	return opts
}

// CreateWallet creates a wallet with the given wallet file name and options.
// A address will be automatically generated by default.
func (serv *Service) CreateWallet(wltName string, options Options) (Wallet, error) {
	serv.Lock()
	defer serv.Unlock()
	if !serv.config.EnableWalletAPI {
		return nil, ErrWalletAPIDisabled
	}
	if wltName == "" {
		wltName = serv.generateUniqueWalletFilename()
	}

	options = serv.updateOptions(options)
	return serv.loadWallet(wltName, options)
}

func (serv *Service) createWallet(wltName string, options Options) (Wallet, error) {
	creator, ok := serv.creators[options.Type]
	if !ok {
		return nil, ErrInvalidWalletType
	}

	return creator.Create(wltName, options.Label, options.Seed, options)
}

// loadWallet loads wallet from seed and scan the first N addresses
func (serv *Service) loadWallet(wltName string, options Options) (Wallet, error) {
	options = serv.updateOptions(options)

	w, err := serv.createWallet(wltName, options)
	if err != nil {
		return nil, err
	}

	fingerprint := w.Fingerprint()
	// Note: collection wallets do not have fingerprints
	if fingerprint != "" {
		if _, ok := serv.fingerprints[fingerprint]; ok {
			logger.WithFields(logrus.Fields{
				"walletType":  w.Type(),
				"fingerprint": fingerprint,
			}).Error("fingerprint conflict")
			return nil, fmt.Errorf("fingerprint conflict for %q wallet", w.Type())
			//switch w.Type() {
			//case WalletTypeDeterministic, WalletTypeBip44:
			//	return nil, ErrSeedUsed
			//case WalletTypeXPub:
			//	return nil, ErrXPubKeyUsed
			//default:
			//	logger.WithFields(logrus.Fields{
			//		"walletType":  w.Type(),
			//		"fingerprint": fingerprint,
			//	}).Panic("Unhandled wallet type after fingerprint conflict")
			//}
		}
	}

	if err := serv.wallets.add(w); err != nil {
		return nil, err
	}

	if err := Save(w, serv.config.WalletDir); err != nil {
		// If save fails, remove the added wallet
		serv.wallets.remove(w.Filename())
		return nil, err
	}

	if fingerprint != "" {
		serv.fingerprints[fingerprint] = w.Filename()
	}

	return w.Clone(), nil
}

func (serv *Service) generateUniqueWalletFilename() string {
	wltName := NewWalletFilename()
	for {
		if w := serv.wallets.get(wltName); w == nil {
			break
		}
		wltName = NewWalletFilename()
	}

	return wltName
}

// EncryptWallet encrypts wallet with password
func (serv *Service) EncryptWallet(wltID string, password []byte) (Wallet, error) {
	serv.Lock()
	defer serv.Unlock()
	if !serv.config.EnableWalletAPI {
		return nil, ErrWalletAPIDisabled
	}

	w, err := serv.getWallet(wltID)
	if err != nil {
		return nil, err
	}

	if w.IsEncrypted() {
		return nil, ErrWalletEncrypted
	}

	if err := w.Lock(password); err != nil {
		return nil, err
	}

	// Saves to disk
	if err := Save(w, serv.config.WalletDir); err != nil {
		return nil, err
	}

	// Updates wallets in memory
	serv.wallets.set(w)
	return w, nil
}

// DecryptWallet decrypts wallet with password
// TODO: this function will be deprecated in future.
func (serv *Service) DecryptWallet(wltID string, password []byte) (Wallet, error) {
	serv.Lock()
	defer serv.Unlock()
	if !serv.config.EnableWalletAPI {
		return nil, ErrWalletAPIDisabled
	}

	w, err := serv.getWallet(wltID)
	if err != nil {
		return nil, err
	}

	// Returns error if wallet is not encrypted
	if !w.IsEncrypted() {
		return nil, ErrWalletNotEncrypted
	}

	// Unlocks the wallet
	unlockWlt, err := w.Unlock(password)
	if err != nil {
		return nil, err
	}

	// Updates the wallet file
	if err := Save(unlockWlt, serv.config.WalletDir); err != nil {
		return nil, err
	}

	// Sets the decrypted wallet in memory
	serv.wallets.set(unlockWlt)
	return unlockWlt, nil
}

// NewAddresses generate address entries in given wallet,
// return nil if wallet does not exist.
// Set password as nil if the wallet is not encrypted, otherwise the password must be provided.
// func (serv *Service) NewAddresses(wltID string, password []byte, num uint64) ([]cipher.Address, error) {
// 	serv.Lock()
// 	defer serv.Unlock()

// 	if !serv.config.EnableWalletAPI {
// 		return nil, ErrWalletAPIDisabled
// 	}

// 	w, err := serv.getWallet(wltID)
// 	if err != nil {
// 		return nil, err
// 	}

// 	var addrs []cipher.Address
// 	f := func(wlt Wallet) error {
// 		var err error
// 		addrs, err = wlt.GenerateSkycoinAddresses(num)
// 		return err
// 	}

// 	if w.IsEncrypted() {
// 		if err := GuardUpdate(w, password, f); err != nil {
// 			return nil, err
// 		}
// 	} else {
// 		if len(password) != 0 {
// 			return nil, ErrWalletNotEncrypted
// 		}

// 		if err := f(w); err != nil {
// 			return nil, err
// 		}
// 	}

// 	// Checks if the wallet file is writable
// 	wf := filepath.Join(serv.config.WalletDir, w.Filename())
// 	if !file.IsWritable(wf) {
// 		return nil, ErrWalletPermission
// 	}

// 	// Save the wallet first
// 	if err := Save(w, serv.config.WalletDir); err != nil {
// 		return nil, err
// 	}

// 	serv.wallets.set(w)

// 	return addrs, nil
// }

// NewAddresses generate addresses
func (serv *Service) NewAddresses(wltID string, password []byte, num uint64, options ...Option) ([]cipher.Address, error) {
	serv.Lock()
	defer serv.Unlock()

	if !serv.config.EnableWalletAPI {
		return nil, ErrWalletAPIDisabled
	}

	w, err := serv.getWallet(wltID)
	if err != nil {
		return nil, err
	}

	var addrs []cipher.Addresser
	f := func(w Wallet) error {
		var err error
		addrs, err = w.GenerateAddresses(num, options...)
		return err
	}

	// TODO: Test this
	if w.IsEncrypted() {
		// Bip44 can generate addresses without unlocking the wallet
		// This will generate addresses on bip44 external chain
		if w.Type() == WalletTypeBip44 {
			if err := f(w); err != nil {
				return nil, err
			}
		} else {
			if err := GuardUpdate(w, password, f); err != nil {
				return nil, err
			}
		}
	} else {
		if len(password) != 0 {
			return nil, ErrWalletNotEncrypted
		}

		if err := f(w); err != nil {
			return nil, err
		}
	}

	// Checks if the wallet file is writable
	wf := filepath.Join(serv.config.WalletDir, w.Filename())
	if !file.IsWritable(wf) {
		return nil, ErrWalletPermission
	}

	// Save the wallet first
	if err := Save(w, serv.config.WalletDir); err != nil {
		return nil, err
	}

	serv.wallets.set(w)
	return convertToSkyAddrs(addrs), nil
}

// ScanAddresses scan ahead addresses to see if contains balance.
func (serv *Service) ScanAddresses(wltID string, password []byte, num uint64, tf TransactionsFinder) ([]cipher.Address, error) {
	serv.Lock()
	defer serv.Unlock()
	if !serv.config.EnableWalletAPI {
		return nil, ErrWalletAPIDisabled
	}

	w, err := serv.getWallet(wltID)
	if err != nil {
		return nil, err
	}

	var addrs []cipher.Addresser
	f := func(w Wallet) error {
		var err error
		addrs, err = w.ScanAddresses(num, tf)
		return err
	}

	// For bip44 wallets, there is no need to unlock the wallets even it is encrypted.
	if w.Type() == WalletTypeBip44 {
		if len(password) != 0 {
			return nil, NewError(errors.New("password is not required for scanning bip44 wallet addresses"))
		}

		if err := f(w); err != nil {
			return nil, err
		}
	} else {
		if w.IsEncrypted() {
			if err := GuardUpdate(w, password, f); err != nil {
				return nil, err
			}
		} else {
			if len(password) != 0 {
				return nil, ErrWalletNotEncrypted
			}

			if err := f(w); err != nil {
				return nil, err
			}
		}
	}

	// Checks if the wallet file is writable
	wf := filepath.Join(serv.config.WalletDir, w.Filename())
	if !file.IsWritable(wf) {
		return nil, ErrWalletPermission
	}

	// Saves the wallet to disk
	if err := Save(w, serv.config.WalletDir); err != nil {
		return nil, err
	}

	// Updates wallet in memory
	serv.wallets.set(w)

	// return new generated addresses
	return convertToSkyAddrs(addrs), nil
}

// GetSkycoinAddresses returns all addresses in given wallet
// func (serv *Service) GetSkycoinAddresses(wltID string) ([]cipher.Address, error) {
// 	serv.RLock()
// 	defer serv.RUnlock()
// 	if !serv.config.EnableWalletAPI {
// 		return nil, ErrWalletAPIDisabled
// 	}

// 	w, err := serv.getWallet(wltID)
// 	if err != nil {
// 		return nil, err
// 	}

// 	return w.GetSkycoinAddresses()
// }

// GetAddresses returns all addresses of the selected wallet
func (serv *Service) GetAddresses(wltID string, options ...Option) ([]cipher.Address, error) {
	serv.RLock()
	defer serv.RUnlock()
	if !serv.config.EnableWalletAPI {
		return nil, ErrWalletAPIDisabled
	}

	w, err := serv.getWallet(wltID)
	if err != nil {
		return nil, err
	}
	addrs, err := w.GetAddresses(options...)
	if err != nil {
		return nil, err
	}

	return convertToSkyAddrs(addrs), nil
}

// GetWallet returns wallet by id
func (serv *Service) GetWallet(wltID string) (Wallet, error) {
	serv.RLock()
	defer serv.RUnlock()
	if !serv.config.EnableWalletAPI {
		return nil, ErrWalletAPIDisabled
	}

	return serv.getWallet(wltID)
}

// returns the clone of the wallet of given id
func (serv *Service) getWallet(wltID string) (Wallet, error) {
	w := serv.wallets.get(wltID)
	if w == nil {
		return nil, ErrWalletNotExist
	}
	return w.Clone(), nil
}

// GetWallets returns all wallet clones
func (serv *Service) GetWallets() (Wallets, error) {
	serv.RLock()
	defer serv.RUnlock()
	if !serv.config.EnableWalletAPI {
		return nil, ErrWalletAPIDisabled
	}

	wlts := make(Wallets, len(serv.wallets))
	for k, w := range serv.wallets {
		wlts[k] = w.Clone()
	}
	return wlts, nil
}

// UpdateWalletLabel updates the wallet label
func (serv *Service) UpdateWalletLabel(wltID, label string) error {
	serv.Lock()
	defer serv.Unlock()
	if !serv.config.EnableWalletAPI {
		return ErrWalletAPIDisabled
	}

	w, err := serv.getWallet(wltID)
	if err != nil {
		return err
	}

	w.SetLabel(label)

	if err := Save(w, serv.config.WalletDir); err != nil {
		return err
	}

	serv.wallets.set(w)
	return nil
}

// UnloadWallet removes wallet of given wallet id from the service
func (serv *Service) UnloadWallet(wltID string) error {
	serv.Lock()
	defer serv.Unlock()
	if !serv.config.EnableWalletAPI {
		return ErrWalletAPIDisabled
	}

	wlt := serv.wallets.get(wltID)
	if wlt != nil {
		if fp := wlt.Fingerprint(); fp != "" {
			delete(serv.fingerprints, fp)
		}
	}

	serv.wallets.remove(wltID)
	return nil
}

func (serv *Service) setWallets(wlts Wallets) {
	serv.wallets = wlts

	for wltID, wlt := range wlts {
		if fp := wlt.Fingerprint(); fp != "" {
			serv.fingerprints[fp] = wltID
		}
	}
}

// GetWalletSeed returns seed and seed passphrase of encrypted wallet of given wallet id
// Returns ErrWalletNotEncrypted if it's not encrypted
func (serv *Service) GetWalletSeed(wltID string, password []byte) (string, string, error) {
	serv.RLock()
	defer serv.RUnlock()
	if !serv.config.EnableWalletAPI {
		return "", "", ErrWalletAPIDisabled
	}

	if !serv.config.EnableSeedAPI {
		return "", "", ErrSeedAPIDisabled
	}

	w, err := serv.getWallet(wltID)
	if err != nil {
		return "", "", err
	}

	if !w.IsEncrypted() {
		return "", "", ErrWalletNotEncrypted
	}

	var seed, seedPassphrase string
	if err := GuardView(w, password, func(wlt Wallet) error {
		seed = wlt.Seed()
		seedPassphrase = wlt.SeedPassphrase()
		return nil
	}); err != nil {
		return "", "", err
	}

	return seed, seedPassphrase, nil
}

// UpdateSecrets opens a wallet for modification of secret data and saves it safely
func (serv *Service) UpdateSecrets(wltID string, password []byte, f func(Wallet) error) error {
	serv.Lock()
	defer serv.Unlock()
	if !serv.config.EnableWalletAPI {
		return ErrWalletAPIDisabled
	}

	w, err := serv.getWallet(wltID)
	if err != nil {
		return err
	}

	if w.IsEncrypted() {
		if err := GuardUpdate(w, password, f); err != nil {
			return err
		}
	} else if len(password) != 0 {
		return ErrWalletNotEncrypted
	} else {
		if err := f(w); err != nil {
			return err
		}
	}

	// Save the wallet to disk
	if err := Save(w, serv.config.WalletDir); err != nil {
		return err
	}

	serv.wallets.set(w)

	return nil
}

// Update opens a wallet for modification of non-secret data and saves it safely
func (serv *Service) Update(wltID string, f func(Wallet) error) error {
	serv.Lock()
	defer serv.Unlock()
	if !serv.config.EnableWalletAPI {
		return ErrWalletAPIDisabled
	}

	w, err := serv.getWallet(wltID)
	if err != nil {
		return err
	}

	if err := f(w); err != nil {
		return err
	}

	// Save the wallet to disk
	if err := Save(w, serv.config.WalletDir); err != nil {
		return err
	}

	serv.wallets.set(w)

	return nil
}

// ViewSecrets opens a wallet for reading secret data
func (serv *Service) ViewSecrets(wltID string, password []byte, f func(Wallet) error) error {
	serv.RLock()
	defer serv.RUnlock()
	if !serv.config.EnableWalletAPI {
		return ErrWalletAPIDisabled
	}

	w, err := serv.getWallet(wltID)
	if err != nil {
		return err
	}

	if w.IsEncrypted() {
		return GuardView(w, password, f)
	} else if len(password) != 0 {
		return ErrWalletNotEncrypted
	} else {
		return f(w)
	}
}

// View opens a wallet for reading non-secret data
func (serv *Service) View(wltID string, f func(Wallet) error) error {
	serv.RLock()
	defer serv.RUnlock()
	if !serv.config.EnableWalletAPI {
		return ErrWalletAPIDisabled
	}

	w, err := serv.getWallet(wltID)
	if err != nil {
		return err
	}

	return f(w)
}

// RecoverWallet recovers an encrypted wallet from seed.
// The recovered wallet will be encrypted with the new password, if provided.
func (serv *Service) RecoverWallet(wltName, seed, seedPassphrase string, password []byte, options ...Option) (Wallet, error) {
	serv.Lock()
	defer serv.Unlock()
	if !serv.config.EnableWalletAPI {
		return nil, ErrWalletAPIDisabled
	}

	w, err := serv.getWallet(wltName)
	if err != nil {
		return nil, err
	}

	if !w.IsEncrypted() {
		return nil, ErrWalletNotEncrypted
	}

	switch w.Type() {
	case WalletTypeDeterministic, WalletTypeBip44:
	default:
		return nil, ErrWalletTypeNotRecoverable
	}

	// Create a wallet from this seed and compare the fingerprint
	w2, err := serv.createWallet(wltName, Options{
		Type:           w.Type(),
		Coin:           w.Coin(),
		Bip44Coin:      w.Bip44Coin(),
		Seed:           seed,
		SeedPassphrase: seedPassphrase,
		GenerateN:      1,
	})
	if err != nil {
		err = NewError(fmt.Errorf("RecoverWallet failed to create temporary wallet for fingerprint comparison: %v", err))
		logger.Critical().WithError(err).Error()
		return nil, err
	}
	if w.Fingerprint() != w2.Fingerprint() {
		return nil, ErrWalletRecoverSeedWrong
	}

	l, err := w.EntriesLen(options...)
	if err != nil {
		return nil, err
	}

	// Create a new wallet with the same number of addresses, encrypting if needed
	w3, err := serv.createWallet(wltName, Options{
		Type:           w.Type(),
		Coin:           w.Coin(),
		Label:          w.Label(),
		Seed:           seed,
		SeedPassphrase: seedPassphrase,
		Encrypt:        len(password) != 0,
		Password:       password,
		CryptoType:     w.CryptoType(),
		Bip44Coin:      w.Bip44Coin(),
		GenerateN:      uint64(l),
	})
	if err != nil {
		return nil, err
	}

	// Preserve the timestamp of the old wallet
	w3.SetTimestamp(w.Timestamp())

	// Save to disk
	if err := Save(w3, serv.config.WalletDir); err != nil {
		return nil, err
	}

	serv.wallets.set(w3)

	return w3.Clone(), nil
}
