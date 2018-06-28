import { WalletsPage } from './wallets.po';

fdescribe('Wallets', () => {
  const page = new WalletsPage();

  it('should display title', () => {
    page.navigateTo();
    expect<any>(page.getHeaderText()).toEqual('Wallets');
  });

  it('should show create wallet', () => {
    expect<any>(page.showAddWallet()).toEqual(true);
    expect<any>(page.getWalletModalTitle()).toEqual('Create Wallet');
  });

  it('should validate create wallet, seed mismatch', () => {
    expect<any>(page.fillWalletForm('Test', 'seed', 'seed2')).toEqual(false);
  });

  it('should validate create wallet, empty label', () => {
    expect<any>(page.fillWalletForm('', 'seed', 'seed')).toEqual(false);
  });

  it('should create wallet', () => {
    expect<any>(page.createWallet()).toEqual(true);
    page.waitForWalletToBeCreated();
  });

  it('should show load wallet', () => {
    expect<any>(page.showLoadWallet()).toEqual(true);
    expect<any>(page.getWalletModalTitle()).toEqual('Load Wallet');
  });

  it('should validate load wallet, seed', () => {
    expect<any>(page.fillWalletForm('Test', ' ', null)).toEqual(false);
  });

  it('should validate load wallet, empty label', () => {
    expect<any>(page.fillWalletForm(' ', 'seed', null)).toEqual(false);
  });

  it('should load wallet', () => {
    expect<any>(page.loadWallet()).toEqual(true);
    page.waitForWalletToBeCreated();
  });

  it('should expand wallet', () => {
    expect<any>(page.expandWallet()).toEqual(true);
  });

  it('should show wallet QR modal', () => {
    expect<any>(page.showQrDialog()).toEqual(true);
  });

  it('should hide wallet QR modal', () => {
    expect<any>(page.hideQrDialog()).toEqual(false);
  });

  it('should add address to wallet', () => {
    expect<any>(page.addAddress()).toEqual(true);
  });

  it('should hide empty address', () => {
    expect<any>(page.hideEmptyAddress()).toEqual(0);
  });

  it('should show empty address', () => {
    expect<any>(page.showEmptyAddress()).toEqual(true);
  });

  it('should show change wallet name modal', () => {
    expect<any>(page.showChangeWalletName()).toEqual(true);
  });

  it('should change wallet name', () => {
    expect<any>(page.changeWalletName()).toEqual(true);
  });

  it('should encrypt wallet', () => {
    expect<any>(page.canEncrypt()).toEqual(true);
  });

  it('should decrypt wallet', () => {
    expect<any>(page.canDecrypt()).toEqual(true);
  });

  it('should display price information', () => {
    expect<any>(page.showPriceInformation()).toEqual(true);
  });
});
