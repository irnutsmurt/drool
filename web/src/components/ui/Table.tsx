import type { ReactNode } from "react";
import { cx } from "./cx";

export interface Column<T> {
  header: string;
  cell: (row: T) => ReactNode;
  className?: string;
  /** Narrow screens: this cell titles the row's card (default: the first
   * column with a header). */
  primary?: boolean;
}

/** A table. On narrow screens each row becomes a stacked card of
 * "Header: value" lines ("cards", the default), or the table keeps its
 * columns and scrolls sideways where the columns belong together
 * ("scroll"). */
export function Table<T>({ columns, rows, rowKey, empty, mobile = "cards", caption }: {
  columns: Column<T>[];
  rows: T[];
  rowKey: (row: T) => string;
  empty?: ReactNode;
  mobile?: "cards" | "scroll";
  caption?: string;
}) {
  const primary = columns.some((c) => c.primary) ? columns.findIndex((c) => c.primary) : columns.findIndex((c) => c.header !== "");
  return (
    <div className="overflow-x-auto">
      <table className={cx("w-full text-left text-sm", mobile === "cards" ? "rtable" : "max-md:min-w-[36rem]")}>
        {caption && <caption className="sr-only">{caption}</caption>}
        <thead>
          <tr className="border-b border-line text-xs uppercase tracking-wide text-muted">
            {columns.map((c, i) => (
              <th key={c.header || `col${i}`} scope="col" className="px-3 py-2 font-medium">
                {c.header || <span className="sr-only">Actions</span>}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 ? (
            <tr>
              <td colSpan={columns.length} className="rt-empty px-3 py-6 text-center text-muted">
                {empty ?? "Nothing here yet."}
              </td>
            </tr>
          ) : (
            rows.map((r) => (
              <tr key={rowKey(r)} className="border-b border-line last:border-0">
                {columns.map((c, i) => (
                  <td key={c.header || `col${i}`} data-label={c.header} data-primary={i === primary ? "" : undefined} className={c.className ?? "px-3 py-2"}>
                    {c.cell(r)}
                  </td>
                ))}
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}
