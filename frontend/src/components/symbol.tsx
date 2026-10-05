import type { CSSProperties, HTMLAttributes } from "react";

import { cn } from "@/lib/utils";

type SymbolProps = Omit<HTMLAttributes<HTMLSpanElement>, "style"> & {
  /** Material Symbols ligature name, e.g. "verified_user" */
  name: string;
  /** visual size in px (mirrors the template's `text-[Npx]` icon sizing) */
  size?: number | string;
  filled?: boolean;
  weight?: number;
  style?: CSSProperties;
};

/**
 * Renders a Material Symbols Outlined ligature as inline text, matching the
 * icon system used by template.html (Google Material Symbols webfont).
 */
export function Symbol({
  name,
  size = 20,
  filled = false,
  weight = 400,
  className,
  style,
  ...rest
}: SymbolProps) {
  const dimension = typeof size === "number" ? `${size}px` : size;

  return (
    <span
      aria-hidden="true"
      className={cn(
        filled
          ? "material-symbols-outlined-filled"
          : "material-symbols-outlined",
        className,
      )}
      style={{
        fontSize: dimension,
        width: dimension,
        height: dimension,
        fontVariationSettings: `'FILL' ${filled ? 1 : 0}, 'wght' ${weight}, 'GRAD' 0, 'opsz' 24`,
        ...style,
      }}
      {...rest}
    >
      {name}
    </span>
  );
}
