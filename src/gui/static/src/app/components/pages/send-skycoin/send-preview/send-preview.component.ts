import { Component, EventEmitter, Input, OnDestroy, Output, ViewChild } from '@angular/core';
import { WalletService } from '../../../../services/wallet.service';
import { ButtonComponent } from '../../../layout/button/button.component';
import { MatDialogConfig, MatDialog } from '@angular/material/dialog';
import { getHardwareWalletErrorMsg } from '../../../../utils/errors';
import { PreviewTransaction, ConfirmationData } from '../../../../app.datatypes';
import { SubscriptionLike } from 'rxjs';
import { PasswordDialogComponent } from '../../../layout/password-dialog/password-dialog.component';
import { HwWalletService } from '../../../../services/hw-wallet.service';
import { TranslateService } from '@ngx-translate/core';
import { MsgBarService } from '../../../../services/msg-bar.service';
import { mergeMap } from 'rxjs/operators';
import { CopyRawTxData, CopyRawTxComponent } from '../offline-dialogs/implementations/copy-raw-tx.component';
import { showConfirmationModal } from '../../../../utils';

@Component({
  selector: 'app-send-preview',
  templateUrl: './send-preview.component.html',
  styleUrls: ['./send-preview.component.scss'],
})
export class SendVerifyComponent implements OnDestroy {
  @ViewChild('sendButton', { static: false }) sendButton: ButtonComponent;
  @ViewChild('backButton', { static: false }) backButton: ButtonComponent;
  @Input() transaction: PreviewTransaction;
  @Output() onBack = new EventEmitter<boolean>();

  private sendSubscription: SubscriptionLike;

  constructor(
    private walletService: WalletService,
    private msgBarService: MsgBarService,
    private dialog: MatDialog,
    private hwWalletService: HwWalletService,
    private translate: TranslateService,
  ) {}

  ngOnDestroy() {
    this.msgBarService.hide();

    if (this.sendSubscription) {
      this.sendSubscription.unsubscribe();
    }
  }

  back() {
    this.onBack.emit(false);
  }

  send() {
    if (this.sendButton.isLoading()) {
      return;
    }

    this.msgBarService.hide();
    this.sendButton.resetState();

    if (!this.transaction.wallet) {
      const data: CopyRawTxData = {
        rawTx: this.transaction.encoded,
        isUnsigned: true,
      };

      const config = new MatDialogConfig();
      config.width = '566px';
      config.data = data;

      this.dialog.open(CopyRawTxComponent, config).afterClosed().subscribe(() => {
        const confirmationData: ConfirmationData = {
          text: 'offline-transactions.copy-tx.reset-confirmation',
          headerText: 'confirmation.header-text',
          confirmButtonText: 'confirmation.confirm-button',
          cancelButtonText: 'confirmation.cancel-button',
        };

        showConfirmationModal(this.dialog, confirmationData).afterClosed().subscribe(confirmationResult => {
          if (confirmationResult) {
            this.onBack.emit(true);
          }
        });
      });

      return;
    }

    if (this.transaction.wallet.encrypted && !this.transaction.wallet.isHardware) {
      const config = new MatDialogConfig();
      config.data = {
        wallet: this.transaction.wallet,
      };

      this.dialog.open(PasswordDialogComponent, config).componentInstance.passwordSubmit
        .subscribe(passwordDialog => {
          this.finishSending(passwordDialog);
        });
    } else {
      if (!this.transaction.wallet.isHardware) {
        this.finishSending();
      } else {
        this.showBusy();
        this.sendSubscription = this.hwWalletService.checkIfCorrectHwConnected(this.transaction.wallet.addresses[0].address).subscribe(
          () => this.finishSending(),
          err => this.showError(getHardwareWalletErrorMsg(this.translate, err)),
        );
      }
    }
  }

  private showBusy() {
    this.sendButton.setLoading();
    this.backButton.setDisabled();
  }

  private finishSending(passwordDialog?: any) {
    this.showBusy();

    const note = this.transaction.note.trim();

    this.sendSubscription = this.walletService.signTransaction(
      this.transaction.wallet,
      passwordDialog ? passwordDialog.password : null,
      this.transaction,
    ).pipe(mergeMap(result => {
      if (passwordDialog) {
        passwordDialog.close();
      }

      return this.walletService.injectTransaction(result.encoded, note);
    })).subscribe(noteSaved => {
      if (note && !noteSaved) {
        setTimeout(() => this.msgBarService.showWarning(this.translate.instant('send.error-saving-note')));
      } else {
        setTimeout(() => this.msgBarService.showDone('send.sent'));
      }

      this.walletService.startDataRefreshSubscription();

      this.onBack.emit(true);
    }, error => {
      if (passwordDialog) {
        passwordDialog.error(error);
      }

      if (error && error.result) {
        this.showError(getHardwareWalletErrorMsg(this.translate, error));
      } else {
        this.showError(error);
      }
    });
  }

  private showError(error) {
    this.msgBarService.showError(error);
    this.sendButton.resetState();
    this.backButton.resetState().setEnabled();
  }
}
