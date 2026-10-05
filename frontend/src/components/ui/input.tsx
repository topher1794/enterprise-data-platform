import * as React from "react";

import { cn } from "@/lib/utils";

export type InputProps = React.InputHTMLAttributes<HTMLInputElement> & {
  icon?: React.ReactNode;
  trailing?: React.ReactNode;
};

const Input = React.forwardRef<HTMLInputElement, InputProps>(
  ({ className, type, icon, trailing, ...props }, ref) => {
    return (
      <div className="relative flex items-center">
        {icon ? (
          <span
            className="material-symbols-outlined pointer-events-none absolute left-3 text-[18px] text-secondary"
            aria-hidden="true"
          >
            {icon}
          </span>
        ) : null}
        <input
          type={type}
          ref={ref}
          className={cn(
            "w-full rounded-lg bg-surface-container-low py-2 text-body-md text-on-surface outline-none transition-all placeholder:text-outline",
            "focus:bg-surface-container-lowest focus:shadow-[0_0_0_2px_var(--primary)]",
            "disabled:cursor-not-allowed disabled:opacity-50",
            icon ? "pl-10" : "px-3",
            trailing ? "pr-10" : "pr-3",
            className,
          )}
          {...props}
        />
        {trailing ? (
          <span className="absolute right-3 flex items-center">{trailing}</span>
        ) : null}
      </div>
    );
  },
);
Input.displayName = "Input";

export { Input };
