import { CopyButton } from "./CopyButton";

/** Monospace text with a copy button. */
export function CodeBlock({ text }: { text: string }) {
  return (
    <div className="flex items-start gap-2 rounded-ui bg-surface-2 p-3">
      <code className="min-w-0 flex-1 break-all font-mono text-xs">{text}</code>
      <CopyButton text={text} />
    </div>
  );
}
