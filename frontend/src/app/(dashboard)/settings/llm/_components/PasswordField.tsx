"use client";

import { useState } from "react";
import { Eye, EyeOff } from "lucide-react";
import { Input } from "@/components/ui/input";

export interface PasswordFieldProps {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  ariaLabel?: string;
  autoFocus?: boolean;
  disabled?: boolean;
  id?: string;
}

export function PasswordField({
  value,
  onChange,
  placeholder,
  ariaLabel,
  autoFocus,
  disabled,
  id,
}: PasswordFieldProps) {
  const [show, setShow] = useState(false);

  return (
    <div className="relative flex-1">
      <Input
        id={id}
        type={show ? "text" : "password"}
        placeholder={placeholder}
        aria-label={ariaLabel}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        autoFocus={autoFocus}
        disabled={disabled}
        className="h-8 pr-9 font-mono text-xs"
      />
      <button
        type="button"
        aria-label={show ? "Hide key" : "Show key"}
        onClick={() => setShow((prev) => !prev)}
        className="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
      >
        {show ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
      </button>
    </div>
  );
}
