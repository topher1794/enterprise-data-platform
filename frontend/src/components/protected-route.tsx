import * as React from "react";
import { Navigate, useLocation } from "react-router-dom";

import { useAuth } from "@/context/auth-context";
import { Symbol } from "@/components/symbol";

export function ProtectedRoute({
  children,
}: {
  children: React.ReactNode;
}) {
  const { user, isLoading } = useAuth();
  const location = useLocation();

  if (isLoading) {
    return (
      <div className="flex min-h-screen flex-col items-center justify-center gap-3 bg-surface">
        <Symbol
          name="sync"
          size={22}
          className="animate-spin text-primary"
        />
        <span className="font-label-xs uppercase text-on-surface-variant">
          Restoring secure session
        </span>
      </div>
    );
  }

  if (!user) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  }

  return <>{children}</>;
}

export function PublicOnlyRoute({
  children,
}: {
  children: React.ReactNode;
}) {
  const { user, isLoading } = useAuth();

  if (isLoading) return null;
  if (user) return <Navigate to="/dashboard" replace />;

  return <>{children}</>;
}
