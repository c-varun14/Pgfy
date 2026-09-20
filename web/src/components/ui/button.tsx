import * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "../../lib/utils";

const variants = cva("button", {
  variants: {
    variant: {
      primary: "button-primary",
      default: "button-primary",
      secondary: "button-secondary",
      outline: "button-secondary",
      ghost: "button-ghost",
      danger: "button-danger",
    },
    size: { sm: "button-sm", md: "button-md" },
  },
  defaultVariants: { variant: "primary", size: "md" },
});
export function Button({
  className,
  variant,
  size,
  loading = false,
  asChild = false,
  ...props
}: React.ComponentProps<"button"> &
  VariantProps<typeof variants> & { asChild?: boolean; loading?: boolean }) {
  const Component = asChild ? Slot : "button";
  return (
    <Component className={cn(variants({ variant, size }), className)} aria-busy={loading || undefined} disabled={loading || props.disabled} {...props} />
  );
}
