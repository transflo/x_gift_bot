"use client"

import * as React from "react"
import { useTheme } from "next-themes"

type TurnstileOptions = {
  sitekey: string
  action?: string
  theme?: "light" | "dark" | "auto"
  callback?: (token: string) => void
  "expired-callback"?: () => void
  "error-callback"?: () => void
}

type TurnstileApi = {
  render: (element: HTMLElement, options: TurnstileOptions) => string
  remove: (id: string) => void
  reset: (id?: string) => void
}

declare global {
  interface Window {
    turnstile?: TurnstileApi
    onXGiftTurnstileLoad?: () => void
  }
}

function loadScript(): Promise<void> {
  if (window.turnstile) return Promise.resolve()
  return new Promise((resolve) => {
    const existing = document.getElementById("xgift-turnstile")
    if (existing) {
      window.onXGiftTurnstileLoad = () => resolve()
      return
    }
    window.onXGiftTurnstileLoad = () => resolve()
    const script = document.createElement("script")
    script.id = "xgift-turnstile"
    script.src =
      "https://challenges.cloudflare.com/turnstile/v0/api.js?onload=onXGiftTurnstileLoad&render=explicit"
    script.async = true
    script.defer = true
    document.head.appendChild(script)
  })
}

export function Turnstile({
  siteKey,
  action,
  onToken,
}: {
  siteKey: string
  action: string
  onToken: (token: string) => void
}) {
  const ref = React.useRef<HTMLDivElement>(null)
  const widget = React.useRef<string | null>(null)
  const { resolvedTheme } = useTheme()

  React.useEffect(() => {
    let cancelled = false
    void loadScript().then(() => {
      if (cancelled || !ref.current || !window.turnstile || widget.current) return
      widget.current = window.turnstile.render(ref.current, {
        sitekey: siteKey,
        action,
        theme: resolvedTheme === "dark" ? "dark" : "light",
        callback: onToken,
        "expired-callback": () => onToken(""),
        "error-callback": () => onToken(""),
      })
    })
    return () => {
      cancelled = true
      if (widget.current && window.turnstile) {
        window.turnstile.remove(widget.current)
        widget.current = null
      }
    }
  }, [siteKey, action, onToken, resolvedTheme])

  return <div ref={ref} className="min-h-[65px]" />
}
