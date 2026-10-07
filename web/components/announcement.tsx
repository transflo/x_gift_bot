"use client"

import * as React from "react"
import { AlertTriangleIcon, InfoIcon, MegaphoneIcon } from "lucide-react"

import { api } from "@/lib/api"
import { cn } from "@/lib/utils"

type Announcement = { enabled: boolean; text?: string; level?: string; updated?: number }

// The banner is fetched rather than baked into the export, so the operator can
// change it without rebuilding the image. It renders nothing at all when there
// is no announcement, which is the normal state — an empty strip that reserves
// space would be worse than no banner.
export function Announcement() {
  const [current, setCurrent] = React.useState<Announcement>({ enabled: false })

  React.useEffect(() => {
    let active = true
    api<Announcement>("/api/announcement").then((result) => {
      if (active && result.ok && result.data.enabled) setCurrent(result.data)
    })
    return () => {
      active = false
    }
  }, [])

  if (!current.enabled || !current.text) return null

  const level = current.level ?? "info"
  const Icon = level === "critical" ? AlertTriangleIcon : level === "warning" ? MegaphoneIcon : InfoIcon

  return (
    <div
      role="status"
      className={cn(
        "relative z-10 bg-foreground text-background",
        level === "critical" && "bg-destructive text-white",
      )}
    >
      <div className="mx-auto flex w-full max-w-6xl items-start gap-2.5 px-6 py-2.5 sm:px-10">
        <Icon className="mt-0.5 size-4 shrink-0 opacity-80" aria-hidden />
        <p className="text-sm leading-relaxed">{current.text}</p>
      </div>
    </div>
  )
}
