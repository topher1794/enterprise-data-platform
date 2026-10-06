import * as React from "react";
import { useNavigate } from "react-router-dom";
import { useForm, Controller } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { toast } from "sonner";

import { loginSchema, type LoginFormValues } from "@/lib/validation";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Form,
  FormField,
  FormItem,
  FormLabel,
  FormControl,
  FormMessage,
} from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { PasswordInput } from "./PasswordInput";
import { Checkbox } from "@/components/ui/checkbox";
import { useAuth } from "@/context/auth-context";
import { Symbol } from "@/components/symbol";

type PendingAction = "sso" | "passkey" | null;

export function LoginForm() {
  const navigate = useNavigate();
  const { signIn, signInWithSso, unlockWithBiometrics, isLoading } = useAuth();
  const [pending, setPending] = React.useState<PendingAction>(null);
  const busy = isLoading || pending !== null;

  const form = useForm<LoginFormValues>({
    resolver: zodResolver(loginSchema),
    mode: "onBlur",
    defaultValues: {
      email: "",
      password: "",
      rememberDevice: false,
      requireFido2: false,
    },
  });

  const goToDashboard = React.useCallback(() => {
    navigate("/dashboard", { replace: true });
  }, [navigate]);

  const onSubmit = form.handleSubmit(async (values: LoginFormValues) => {
    try {
      await signIn(values);
      toast.success("Signed in", {
        description: "Welcome back to the Enterprise Data Platform.",
      });
      goToDashboard();
    } catch (error) {
      const message =
        error instanceof Error ? error.message : "Invalid email or password.";
      toast.error("Authentication failed", {
        description: message,
      });
    }
  });

  const handleSso = async () => {
    setPending("sso");
    try {
      await signInWithSso("Okta");
      toast.success("SSO verified", {
        description: "Identity asserted by Okta Identity Gateway.",
      });
      goToDashboard();
    } catch (error) {
      toast.error("SSO sign-in failed", {
        description:
          error instanceof Error
            ? error.message
            : "Could not reach the identity provider.",
      });
    } finally {
      setPending(null);
    }
  };

  const handlePasskey = async () => {
    setPending("passkey");
    try {
      await unlockWithBiometrics();
      toast.success("Passkey verified", {
        description: "Hardware-backed WebAuthn assertion accepted.",
      });
      goToDashboard();
    } catch (error) {
      toast.error("Passkey unlock failed", {
        description:
          error instanceof Error
            ? error.message
            : "No passkey was detected on this device.",
      });
    } finally {
      setPending(null);
    }
  };

  const handleForgotPassword = () => {
    toast("info", {
      description: "Password reset initiation not yet configured.",
      icon: (
        <Symbol name="info" size={18} className="text-tertiary-fixed-dim" />
      ),
    });
  };

  return (
    <Card className="border border-outline-variant/70 p-space-xl shadow-[0_18px_48px_-18px_rgba(11,28,48,0.35)]">
      {/* Corporate SSO */}
      <div className="flex flex-col gap-2">
        <Button
          type="button"
          variant="default"
          className="w-full"
          onClick={handleSso}
          disabled={busy}
        >
          {pending === "sso" ? (
            <Symbol name="sync" size={18} className="animate-spin" />
          ) : (
            <Symbol name="verified" size={18} />
          )}
          <span>Sign in with Corporate SSO</span>
        </Button>
        <p className="flex flex-wrap items-center justify-center gap-x-1.5 gap-y-1 font-label-xs text-on-surface-variant">
          <span>Supported:</span>
          <span className="font-semibold text-secondary">Okta</span>
          <span className="text-outline-variant">•</span>
          <span className="font-semibold text-secondary">Azure AD</span>
          <span className="text-outline-variant">•</span>
          <span className="font-semibold text-secondary">PingIdentity</span>
        </p>
      </div>

      {/* Divider */}
      <div className="relative my-space-md flex items-center">
        <span className="h-px w-full bg-surface-container-high" />
        <span className="absolute left-1/2 -translate-x-1/2 whitespace-nowrap bg-surface-container-lowest px-2.5 font-label-xs uppercase text-on-surface-variant">
          or enterprise credentials
        </span>
      </div>

      <Form {...form}>
        <form noValidate onSubmit={onSubmit} className="space-y-4">
          <FormField
            control={form.control}
            name="email"
            render={({ field }) => (
              <FormItem>
                <FormLabel className="flex items-center justify-between">
                  <span>Work Email</span>
                  <span className="font-normal normal-case text-secondary">
                    LDAP / IAM ID
                  </span>
                </FormLabel>
                <FormControl>
                  <Input
                    icon="mail"
                    type="email"
                    inputMode="email"
                    autoComplete="username"
                    placeholder="alex.chen@datamesh.corp"
                    {...field}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name="password"
            render={({ field }) => (
              <FormItem>
                <div className="flex items-center justify-between">
                  <FormLabel>Password</FormLabel>
                  <button
                    type="button"
                    onClick={handleForgotPassword}
                    className="font-label-xs text-primary transition-colors hover:text-primary-container"
                  >
                    Forgot?
                  </button>
                </div>
                <FormControl>
                  <PasswordInput showStrength {...field} />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />

          <div className="flex flex-col gap-2 pt-1">
            <Controller
              control={form.control}
              name="rememberDevice"
              render={({ field }) => (
                <label
                  className="flex cursor-pointer select-none items-center justify-between rounded-lg px-1 py-0.5 transition-colors hover:bg-surface-container-low"
                  onClick={(event) => {
                    if (
                      event.target instanceof HTMLElement &&
                      event.target.closest("button")
                    ) {
                      return;
                    }
                    event.preventDefault();
                    field.onChange(!field.value);
                  }}
                >
                  <span className="flex items-center gap-2">
                    <Symbol name="devices" size={16} className="text-secondary" />
                    <span className="font-body-sm text-on-surface">
                      Remember device{" "}
                      <span className="text-on-surface-variant">(30 days)</span>
                    </span>
                  </span>
                  <Checkbox
                    checked={field.value}
                    onCheckedChange={(checked) =>
                      field.onChange(checked === true)
                    }
                  />
                </label>
              )}
            />

            <Controller
              control={form.control}
              name="requireFido2"
              render={({ field }) => (
                <label
                  className="flex cursor-pointer select-none items-center justify-between rounded-lg px-1 py-0.5 transition-colors hover:bg-surface-container-low"
                  onClick={(event) => {
                    if (
                      event.target instanceof HTMLElement &&
                      event.target.closest("button")
                    ) {
                      return;
                    }
                    event.preventDefault();
                    field.onChange(!field.value);
                  }}
                >
                  <span className="flex items-center gap-2">
                    <Symbol name="token" size={16} className="text-secondary" />
                    <span className="flex flex-col">
                      <span className="font-body-sm text-on-surface">
                        Require hardware FIDO2 key
                      </span>
                      <span className="font-label-xs text-on-surface-variant">
                        YubiKey / WebAuthn token
                      </span>
                    </span>
                  </span>
                  <Checkbox
                    checked={field.value}
                    onCheckedChange={(checked) =>
                      field.onChange(checked === true)
                    }
                  />
                </label>
              )}
            />
          </div>

          <div className="flex flex-col gap-2 pt-1">
            <Button
              type="submit"
              variant="container"
              disabled={busy}
              className="w-full"
            >
              {isLoading ? (
                <>
                  <Symbol name="sync" size={18} className="animate-spin" />
                  <span>Signing in…</span>
                </>
              ) : (
                <>
                  <span>Sign In to Workspace</span>
                  <Symbol name="arrow_forward" size={18} />
                </>
              )}
            </Button>

            <Button
              type="button"
              variant="secondary"
              onClick={handlePasskey}
              disabled={busy}
              className="w-full"
            >
              {pending === "passkey" ? (
                <Symbol name="sync" size={18} className="animate-spin" />
              ) : (
                <Symbol name="fingerprint" size={20} className="text-primary" />
              )}
              <span className="font-body-sm font-medium">
                Face ID / Touch ID fast unlock
              </span>
            </Button>
          </div>
        </form>
      </Form>

      {/* Trust note */}
      <div className="mt-space-md flex items-start gap-2.5 rounded-lg bg-surface-container-low p-3">
        <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-surface-container text-primary">
          <Symbol name="security" size={16} filled />
        </span>
        <p className="font-body-sm text-on-surface-variant">
          <span className="font-semibold text-on-surface">
            Zero-Trust Access Gateway
          </span>{" "}
          — end-to-end audit logging with HSM-backed key rotation and adaptive
          IP telemetry.
        </p>
      </div>
    </Card>
  );
}
