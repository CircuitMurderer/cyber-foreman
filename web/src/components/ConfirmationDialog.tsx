import {AlertDialog, Button, useOverlayState} from "@heroui/react";
import {cloneElement, type ReactElement} from "react";

interface ConfirmationDialogProps {
  trigger: ReactElement;
  title: string;
  description: string;
  detail?: string;
  confirmLabel: string;
  tone?: "primary" | "danger";
  onConfirm: () => void | Promise<void>;
}

export function ConfirmationDialog({
  trigger, title, description, detail, confirmLabel, tone = "danger", onConfirm
}: ConfirmationDialogProps) {
  const state = useOverlayState();

  function confirm() {
    state.close();
    void onConfirm();
  }

  const controlledTrigger = cloneElement(trigger as ReactElement<{onPress?: () => void}>, {
    onPress: state.open
  });

  return (
    <AlertDialog isOpen={state.isOpen} onOpenChange={state.setOpen}>
      {controlledTrigger}
      <AlertDialog.Backdrop variant="blur">
        <AlertDialog.Container size="sm" placement="center">
          <AlertDialog.Dialog className="confirm-dialog">
            <AlertDialog.Icon status={tone === "danger" ? "danger" : "accent"} />
            <AlertDialog.Header>
              <AlertDialog.Heading>{title}</AlertDialog.Heading>
            </AlertDialog.Header>
            <AlertDialog.Body>
              <p>{description}</p>
              {detail && <code>{detail}</code>}
            </AlertDialog.Body>
            <AlertDialog.Footer>
              <Button variant="secondary" onPress={state.close}>取消</Button>
              <Button variant={tone === "danger" ? "danger" : "primary"} onPress={confirm}>{confirmLabel}</Button>
            </AlertDialog.Footer>
          </AlertDialog.Dialog>
        </AlertDialog.Container>
      </AlertDialog.Backdrop>
    </AlertDialog>
  );
}
