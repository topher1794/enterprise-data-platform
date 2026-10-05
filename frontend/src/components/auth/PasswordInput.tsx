import * as React from "react";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";

export interface PasswordInputProps extends React.InputHTMLAttributes<HTMLInputElement> {
  trailing?: React.ReactNode;
}

export function PasswordInput({
  type = "password",
  ...props
}: PasswordInputProps) {
  const [showPassword, setShowPassword] = React.useState(false);

  return (
    <div>
      <Input
        type={showPassword ? "text" : type}
        autoComplete="current-password"
        placeholder="Enter your password"
        {...props}
        trailing={
          <button
            type="button"
            aria-label="Toggle password visibility"
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
    </div>
  );
}