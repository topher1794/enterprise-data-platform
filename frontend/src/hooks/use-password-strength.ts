import { useMemo } from "react";

export type PasswordStrength = {
  /** 0 = empty, 1 = weak, 2 = good, 3 = strong */
  score: 0 | 1 | 2 | 3;
  label: string;
  /** token used for the active bars */
  barClassName: string;
  textClassName: string;
};

const IDLE: PasswordStrength = {
  score: 0,
  label: "Min 12 char",
  barClassName: "bg-surface-container-high",
  textClassName: "text-outline",
};

export function usePasswordStrength(value: string): PasswordStrength {
  return useMemo(() => {
    if (!value) return IDLE;

    const length = value.length;
    const variety = [/[a-z]/, /[A-Z]/, /[0-9]/, /[^A-Za-z0-9]/].filter((re) =>
      re.test(value),
    ).length;

    if (length < 8) {
      return {
        score: 1,
        label: "Weak",
        barClassName: "bg-error",
        textClassName: "text-error",
      };
    }
    if (length < 12 || variety < 3) {
      return {
        score: 2,
        label: "Good",
        barClassName: "bg-primary-container",
        textClassName: "text-primary-container",
      };
    }
    return {
      score: 3,
      label: "Strong",
      barClassName: "bg-tertiary-container",
      textClassName: "text-tertiary-container",
    };
  }, [value]);
}
