"use client";

import type { InputHTMLAttributes, TextareaHTMLAttributes } from "react";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { useDebouncedTextField } from "@/hooks/useDebouncedTextField";

type DebouncedInputProps = Omit<
  InputHTMLAttributes<HTMLInputElement>,
  "value" | "onChange" | "onBlur"
> & {
  serverValue: string;
  onCommit: (value: string) => void;
  debounceMs?: number;
};

export function DebouncedInput({
  serverValue,
  onCommit,
  debounceMs,
  ...rest
}: DebouncedInputProps) {
  const field = useDebouncedTextField({ serverValue, onCommit, debounceMs });
  return (
    <Input
      {...rest}
      value={field.value}
      onChange={(event) => field.onChange(event.target.value)}
      onBlur={field.onBlur}
    />
  );
}

type DebouncedTextareaProps = Omit<
  TextareaHTMLAttributes<HTMLTextAreaElement>,
  "value" | "onChange" | "onBlur"
> & {
  serverValue: string;
  onCommit: (value: string) => void;
  debounceMs?: number;
};

export function DebouncedTextarea({
  serverValue,
  onCommit,
  debounceMs,
  ...rest
}: DebouncedTextareaProps) {
  const field = useDebouncedTextField({ serverValue, onCommit, debounceMs });
  return (
    <Textarea
      {...rest}
      value={field.value}
      onChange={(event) => field.onChange(event.target.value)}
      onBlur={field.onBlur}
    />
  );
}
