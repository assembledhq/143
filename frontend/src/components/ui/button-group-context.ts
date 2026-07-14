import * as React from "react"

export type ButtonGroupSize = "default" | "xs" | "sm" | "lg"

export const ButtonGroupSizeContext = React.createContext<ButtonGroupSize | null>(null)
