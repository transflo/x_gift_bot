"use client"

import * as React from "react"
import { useTheme } from "next-themes"
import { MoonIcon, SunIcon } from "lucide-react"

import { Button } from "@/components/ui/button"

const emptySubscribe = () => () => {}

export function ThemeToggle() {
  const { resolvedTheme, setTheme } = useTheme()
  // Hydration-safe mount check without a setState-in-effect.
  const mounted = React.useSyncExternalStore(
    emptySubscribe,
    () => true,
    () => false,
  )

  const dark = resolvedTheme === "dark"
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      aria-label={dark ? "切换到浅色模式" : "切换到深色模式"}
      onClick={() => setTheme(dark ? "light" : "dark")}
    >
      {mounted ? (
        dark ? (
          <SunIcon className="size-4" />
        ) : (
          <MoonIcon className="size-4" />
        )
      ) : (
        <SunIcon className="size-4 opacity-0" />
      )}
    </Button>
  )
}
