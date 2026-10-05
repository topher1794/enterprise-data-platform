import * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg font-body-md font-semibold ring-offset-surface transition-all focus-visible:outline-none focus-visible:shadow-[0_0_0_2px_var(--primary)] focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:pointer-events-none disabled:opacity-50 active:scale-[0.99] [&_svg]:pointer-events-none [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default:
          "bg-primary text-on-primary shadow-sm hover:bg-primary-container",
        container:
          "bg-primary-container text-on-primary shadow-sm hover:bg-primary",
        secondary:
          "bg-surface-container text-on-surface hover:bg-surface-container-high",
        outline:
          "border border-outline-variant bg-surface-container-lowest text-on-surface hover:bg-surface-container-low",
        ghost: "text-primary hover:bg-surface-container",
        link: "text-primary underline-offset-4 hover:underline",
        destructive: "bg-error text-on-error shadow-sm hover:opacity-90",
      },
      size: {
        default: "h-auto px-space-md py-2.5",
        sm: "px-3 py-1.5 font-body-sm",
        lg: "px-space-xl py-3 font-body-lg",
        icon: "h-9 w-9",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  },
);

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean;
}

const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, asChild = false, ...props }, ref) => {
    const Comp = asChild ? Slot : "button";
    return (
      <Comp
        className={cn(buttonVariants({ variant, size, className }))}
        ref={ref}
        {...props}
      />
    );
  },
);
Button.displayName = "Button";

export { Button, buttonVariants };
