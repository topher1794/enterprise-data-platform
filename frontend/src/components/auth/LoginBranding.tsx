import * as React from "react";
import { cn } from "@/lib/utils";
import { Symbol } from "@/components/symbol";

const featureHighlights = [
  { name: "Data Source Management", description: "Connect and integrate data from any source" },
  { name: "Pipeline Orchestration", description: "Automate ETL/ELP workflows visually" },
  { name: "Data Quality", description: "Proactive monitoring and anomaly detection" },
  { name: "Data Governance", description: "Policy-based access and lineage tracking" },
] as const;

export function LoginBranding() {
  return (
    <aside className="flex-shrink-0 w-64 flex flex-col p-space-lg bg-surface-container-lowest border-r border-border-variant">
      <div className="flex flex-col items-center space-y-4">
        <div className="flex flex-col items-center space-y-2">
          <span className="font-headline-sm font-semibold tracking-tight text-on-surface">
            EDP
          </span>
          <span className="text-on-surface-variant">Enterprise Data Platform</span>
        </div>

        <h2 className="font-headline-md font-semibold text-on-surface">
          Secure access to your enterprise data.
        </h2>

        <p className="font-body-sm text-on-surface-variant">
          Manage data sources, pipelines, quality, governance, metadata, and analytics
          from one platform.
        </p>
      </div>

      <div className="w-full space-y-2">
        {featureHighlights.map((item) => (
          <div
            key={item.name}
            className={cn(
              "flex items-start space-x-2 rounded-md bg-surface px-3 py-2 font-label-sm",
            )}
          >
            <Symbol
              name="check"
              size={14}
              className="shrink-0 text-primary flex-auto"
            />
            <span className="flex-1 text-on-surface-variant">
              {item.name}
            </span>
          </div>
        ))}
      </div>
    </aside>
  );
}