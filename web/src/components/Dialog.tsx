import { useEffect, useRef } from "react";
import type { ReactNode } from "react";

export function Dialog({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current;
    dialog?.showModal();
    return () => dialog?.close();
  }, []);
  return (
    <dialog
      ref={ref}
      className="dialog"
      aria-labelledby="dialog-title"
      onCancel={onClose}
      onClick={(event) => {
        if (event.target === event.currentTarget) {
          const box = event.currentTarget.getBoundingClientRect();
          if (
            event.clientX < box.left ||
            event.clientX > box.right ||
            event.clientY < box.top ||
            event.clientY > box.bottom
          )
            onClose();
        }
      }}
    >
      <button className="dialog-close" aria-label="关闭" onClick={onClose}>
        ×
      </button>
      <span className="eyebrow">A SMALL MOMENT OF KINDNESS</span>
      <h2 id="dialog-title">{title}</h2>
      {children}
    </dialog>
  );
}
