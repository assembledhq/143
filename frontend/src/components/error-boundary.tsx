"use client"

import { Component, type ReactNode } from "react"

interface Props {
  children: ReactNode
  fallback?: ReactNode
}

interface State {
  hasError: boolean
}

export class ErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props)
    this.state = { hasError: false }
  }

  static getDerivedStateFromError(): State {
    return { hasError: true }
  }

  render() {
    if (this.state.hasError) {
      return this.props.fallback ?? (
        <div className="flex min-h-screen items-center justify-center">
          <div className="text-center space-y-4">
            <h2 className="text-xl font-semibold">Something went wrong</h2>
            <p className="text-muted-foreground">An unexpected error occurred. Please refresh the page.</p>
            <button
              onClick={() => this.setState({ hasError: false })}
              className="inline-flex cursor-pointer items-center justify-center rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90"
            >
              Try again
            </button>
          </div>
        </div>
      )
    }

    return this.props.children
  }
}
