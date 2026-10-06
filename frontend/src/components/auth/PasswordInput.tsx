import * as React from "react";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { Symbol } from "@/components/symbol";
import { usePasswordStrength } from "@/hooks/use-password-strength";

export interface PasswordInputProps
  extends React.InputHTMLAttributes<HTMLInputElement> {
  trailing?: React.ReactNode;
  /** Renders the realtime strength micro-bar below the field. */
  showStrength?: boolean;
}

export const PasswordInput = React.forwardRef<
  HTMLInputElement,
  PasswordInputProps
>(function PasswordInput(
  { type = "password", showStrength = false, value, ...props },
  ref,
) {
  const [showPassword, setShowPassword] = React.useState(false);
  const strength = usePasswordStrength(String(value ?? ""));

  return (
    <div>
      <Input
        ref={ref}
        type={showPassword ? "text" : type}
        icon="key"
        autoComplete="current-password"
        placeholder="••••••••••••"
        value={value}
        {...props}
        trailing={
          <button
            type="button"
            aria-label={showPassword ? "Hide password" : "Show password"}
            aria-pressed={showPassword}
            onClick={() => setShowPassword((v) => !v)}
            className={cn(
              "rounded-md bg-transparent p-1 text-secondary hover:text-on-surface focus:outline-none focus:bg-surface-container-low",
            )}
          >
            <Symbol
              name={showPassword ? "visibility_off" : "visibility"}
              size={18}
            />
          </button>
        }
      />

      {showStrength ? (
        <div className="flex items-center gap-1 pt-1.5" aria-hidden="true">
          {[1, 2, 3].map((step) => (
            <span
              key={step}
              className={cn(
                "h-1 flex-1 rounded-full transition-colors",
                strength.score >= step
                  ? strength.barClassName
                  : "bg-surface-container-high",
              )}
            />
          ))}
          <span
            className={cn("pl-1.5 font-label-xs", strength.textClassName)}
          >
            {strength.label}
          </span>
        </div>
      ) : null}
    </div>
  );
});
