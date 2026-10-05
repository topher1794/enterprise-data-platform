import * as React from "react";
import { cn } from "@/lib/utils";

export interface AuthErrorProps {
  error: string | null;
  setError?: (error: string | null) => void;
}

export function AuthError({ error, setError }: AuthErrorProps) {
  return (
    <p
      className={cn(
        "mt-1.5 text-error font-label-xs",
        "flex items-center space-x-1.5",
      )}
    >
      <Symbol name="error" size={14} className="text-error" />
      {error || ""}
    </p>
  );
}