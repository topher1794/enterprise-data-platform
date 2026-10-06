import { Symbol } from "@/components/symbol";

const featureHighlights = [
  {
    icon: "database",
    name: "Data Source Management",
    description: "Connect and integrate data from any source",
  },
  {
    icon: "workflow",
    name: "Pipeline Orchestration",
    description: "Automate ETL/ELT workflows visually",
  },
  {
    icon: "monitoring",
    name: "Data Quality",
    description: "Proactive monitoring and anomaly detection",
  },
  {
    icon: "policy",
    name: "Data Governance",
    description: "Policy-based access and lineage tracking",
  },
] as const;

export function LoginBranding() {
  return (
    <aside className="relative hidden w-full overflow-hidden bg-[linear-gradient(155deg,#001b57_0%,#003597_48%,#004ac6_100%)] px-10 py-9 text-white lg:flex lg:w-[44%] lg:flex-col xl:w-[46%]">
      {/* Decorative grid + glow layers */}
      <div
        aria-hidden="true"
        className="pointer-events-none absolute inset-0 opacity-[0.16] [background-image:linear-gradient(rgba(255,255,255,0.45)_1px,transparent_1px),linear-gradient(90deg,rgba(255,255,255,0.45)_1px,transparent_1px)] [background-size:44px_44px] [mask-image:radial-gradient(ellipse_at_top_left,black,transparent_72%)]"
      />
      <div
        aria-hidden="true"
        className="pointer-events-none absolute -left-20 -top-24 h-64 w-64 rounded-full bg-primary-fixed/30 blur-3xl"
      />
      <div
        aria-hidden="true"
        className="pointer-events-none absolute -bottom-28 -right-16 h-72 w-72 rounded-full bg-tertiary-fixed/20 blur-3xl"
      />

      {/* Brand identity */}
      <header className="relative flex items-center gap-3">
        <span className="flex h-11 w-11 items-center justify-center rounded-xl bg-white p-2 shadow-sm">
          <img
            src="/brand/edp-mark.svg"
            alt=""
            className="h-full w-full object-contain"
          />
        </span>
        <div className="flex flex-col">
          <span className="flex items-center gap-2">
            <span className="font-headline-sm font-semibold tracking-tight">
              EDP Platform
            </span>
            <span className="rounded bg-white/15 px-1.5 py-0.5 font-label-xs uppercase text-white/90">
              v4.8
            </span>
          </span>
          <span className="font-label-xs uppercase tracking-wide text-white/65">
            Enterprise Data Platform
          </span>
        </div>
      </header>

      {/* Value proposition */}
      <div className="relative flex flex-1 flex-col justify-center gap-6 py-10">
        <span className="inline-flex w-fit items-center gap-1.5 rounded-full bg-white/10 px-2.5 py-1 font-label-xs uppercase tracking-wide text-white/85 ring-1 ring-inset ring-white/15">
          <Symbol name="shield_lock" size={13} />
          Zero-Trust Access Gateway
        </span>

        <div className="space-y-2">
          <h1 className="font-headline-lg text-white">
            Secure access to your enterprise data.
          </h1>
          <p className="max-w-md font-body-lg text-white/75">
            Manage data sources, pipelines, quality, governance, metadata, and
            analytics from a single platform.
          </p>
        </div>

        <ul className="flex flex-col gap-3">
          {featureHighlights.map((item) => (
            <li key={item.name} className="flex items-start gap-3">
              <span className="mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-lg bg-white/10 ring-1 ring-inset ring-white/15">
                <Symbol name={item.icon} size={15} className="text-tertiary-fixed" />
              </span>
              <span className="flex flex-col">
                <span className="font-label-lg font-semibold text-white">
                  {item.name}
                </span>
                <span className="font-body-sm text-white/65">
                  {item.description}
                </span>
              </span>
            </li>
          ))}
        </ul>
      </div>

      {/* Trust footer */}
      <footer className="relative flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <span className="inline-flex items-center gap-1.5 rounded-full bg-white/10 px-2.5 py-1 ring-1 ring-inset ring-white/15">
            <span className="h-1.5 w-1.5 rounded-full bg-tertiary-fixed animate-pulse" />
            <span className="font-label-xs uppercase tracking-wide text-white/85">
              All Systems Operational
            </span>
          </span>
          <span className="inline-flex items-center gap-1.5 rounded-full bg-white/10 px-2.5 py-1 ring-1 ring-inset ring-white/15">
            <Symbol name="verified_user" size={13} filled className="text-tertiary-fixed" />
            <span className="font-label-xs uppercase tracking-wide text-white/85">
              SOC2 Type II
            </span>
          </span>
        </div>
        <p className="font-label-xs font-medium text-white/55">
          © 2026 Enterprise Data Platform Inc. · Encrypted via TLS 1.3 ·
          AES-256 GCM
        </p>
      </footer>
    </aside>
  );
}
