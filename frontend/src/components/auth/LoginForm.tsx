import * as React from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";

import { loginSchema, type LoginFormValues } from "@/lib/validation";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Form, FormField, FormItem, FormLabel, FormControl, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { PasswordInput } from "./PasswordInput";
import { useAuth } from "@/context/auth-context";
import { toast } from "sonner";

export function LoginForm() {
  const navigate = useNavigate();
  const { signIn, isLoading } = useAuth();

  const form = useForm<LoginFormValues>({
    resolver: zodResolver(loginSchema),
    mode: "onBlur",
    defaultValues: {
      email: "",
      password: "",
      rememberDevice: false,
    },
  });

  const onSubmit = form.handleSubmit(async (values: LoginFormValues) => {
    try {
      await signIn(values);
      toast.success("Signed in", {
        description: "Welcome back to the Enterprise Data Platform.",
      });
      navigate("/dashboard", { replace: true });
    } catch (error) {
      const message =
        error instanceof Error ? error.message : "Invalid email or password.";
      toast.error("Authentication failed", {
        description: message,
      });
    }
  });

  return (
    <Card className="p-space-lg">
      <h2 className="font-headline-sm font-semibold text-on-space mb-3">
        Welcome back
      </h2>
      <p className="text-on-surface-variant mb-6">
        Sign in to your Enterprise Data Platform account.
      </p>

      <Form {...form}>
        <form
          noValidate
          onSubmit={form.handleSubmit(async (e) => {
            e.preventDefault();
          })}
          className="space-y-4"
        >
          <FormField
            control={form.control}
            name="email"
            render={({ field }) => (
              <FormItem>
                <FormLabel>Email</FormLabel>
                <FormControl>
                  <Input
                    icon="mail"
                    type="email"
                    inputMode="email"
                    autoComplete="username"
                    placeholder="name@company.com"
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
                <FormLabel>Password</FormLabel>
                <PasswordInput
                  ref={field.ref}
                  {...field}
                />
                <FormMessage />
              </FormItem>
            )}

          />

          <div className="flex items-center justify-between">
            <div className="flex items-center space-x-2">
              <Button
                type="submit"
                variant="container"
                disabled={isLoading}
                className="flex-1"
              >
                {isLoading ? (
                  <span className="align-middle">Signing in...</span>
                ) : (
                  <span>Sign In</span>
                )}
              </Button>
            </div>

            <a
              href="#"
              className="font-label-xs text-primary transition-colors hover:text-primary-container"
            >
              Forgot password?
            </a>
          </div>
        </form>
      </Form>
    </Card>
  );
}