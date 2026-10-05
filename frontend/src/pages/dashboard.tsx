import { toast } from "sonner";

import { Symbol } from "@/components/symbol";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { useAuth } from "@/context/auth-context";

const STATS = [
  { label: "Active Pipelines", value: "128", delta: "+4 this week", icon: "account_tree" },
  { label: "Lakehouse Storage", value: "4.2 PB", delta: "72% of quota", icon: "database" },
  { label: "Mesh Clusters", value: "9", delta: "All healthy", icon: "hub" },
  { label: "Daily Ingest", value: "1.8 TB", delta: "+12.4% vs avg", icon: "move_down" },
] as const;

export default function DashboardPage() {
  const { user, cluster, signOut } = useAuth();

  return (
    <main className="flex min-h-screen w-full flex-col bg-surface pt-safe pb-safe">
      <div className="mx-auto flex w-full max-w-[880px] flex-1 flex-col px-margin py-space-lg">
        <header className="flex items-center justify-between">
          <div className="flex items-center space-x-2">
            <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-surface-container-low shadow-sm">
              <img
                alt="EDP Platform"
                src="/brand/edp-logo.png"
                className="h-full w-full rounded object-contain"
              />
            </div>
            <div className="flex flex-col">
              <span className="font-headline-sm font-semibold tracking-tight text-on-surface">
                Workspace
              </span>
              <span className="font-label-xs uppercase text-on-surface-variant">
                {cluster}
              </span>
            </div>
          </div>

          <div className="flex items-center space-x-2">
            <div className="hidden flex-col items-end sm:flex">
              <span className="font-label-sm font-semibold text-on-surface">
                {user?.displayName}
              </span>
              <span className="font-label-xs text-on-surface-variant">
                {user?.role}
              </span>
            </div>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => {
                signOut();
                toast.success("Signed out of workspace");
              }}
            >
              <Symbol name="logout" size={16} />
              Sign out
            </Button>
          </div>
        </header>

        <div className="mt-space-xl grid grid-cols-1 gap-gutter sm:grid-cols-2">
          {STATS.map((stat) => (
            <Card key={stat.label} className="p-space-md">
              <div className="flex items-start justify-between">
                <div className="flex flex-col space-y-1">
                  <span className="font-label-xs uppercase text-on-surface-variant">
                    {stat.label}
                  </span>
                  <span className="font-headline-md font-semibold text-on-surface">
                    {stat.value}
                  </span>
                  <span className="font-label-xs text-tertiary">
                    {stat.delta}
                  </span>
                </div>
                <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-surface-container text-primary">
                  <Symbol name={stat.icon} size={20} />
                </div>
              </div>
            </Card>
          ))}
        </div>

        <Card className="mt-space-md flex items-start space-x-3 p-space-md">
          <div className="flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-lg bg-surface-container text-primary">
            <Symbol name="construction" size={20} filled />
          </div>
          <div className="flex flex-col space-y-0.5">
            <span className="font-label-sm font-semibold text-on-surface">
              Dashboard placeholder
            </span>
            <p className="font-body-sm text-on-surface-variant">
              Auth is wired end to end (context, guards, protected routes, session
              persistence). Build your real screens on top of{" "}
              <code className="font-mono text-primary">/dashboard</code> — swap
              this stub for your data mesh views.
            </p>
          </div>
        </Card>
      </div>
    </main>
  );
}
