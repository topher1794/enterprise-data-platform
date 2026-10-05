import {
  BrowserRouter,
  Navigate,
  Route,
  Routes,
} from "react-router-dom";
import { Toaster } from "sonner";

import { ProtectedRoute, PublicOnlyRoute } from "@/components/protected-route";
import { AuthProvider } from "@/context/auth-context";
import DashboardPage from "@/pages/dashboard";
import LoginPage from "@/pages/login";

export default function App() {
  return (
    <AuthProvider>
      <BrowserRouter>
        <Routes>
          <Route path="/" element={<Navigate to="/login" replace />} />
          <Route
            path="/login"
            element={
              <PublicOnlyRoute>
                <LoginPage />
              </PublicOnlyRoute>
            }
          />
          <Route
            path="/dashboard"
            element={
              <ProtectedRoute>
                <DashboardPage />
              </ProtectedRoute>
            }
          />
          <Route path="*" element={<Navigate to="/login" replace />} />
        </Routes>

        <Toaster
          position="bottom-center"
          duration={2800}
          toastOptions={{
            unstyled: true,
            classNames: {
              toast:
                "flex items-center space-x-2 rounded-xl bg-inverse-surface px-4 py-2.5 text-inverse-on-surface shadow-lg",
              title: "font-body-sm font-medium",
              description: "font-body-sm",
            },
          }}
        />
      </BrowserRouter>
    </AuthProvider>
  );
}
