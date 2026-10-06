import { Symbol } from "@/components/symbol";
import { cn } from "@/lib/utils";

export function AuthError({ error }: { error: string | null }) {
  return (
    <p
      className={cn("mt-1.5 text-error font-label-xs", "flex items-center space-x-1.5")}
    >
      <Symbol name="error" size={14} className="text-error" />
      {error || ""}
    </p>
  );
}