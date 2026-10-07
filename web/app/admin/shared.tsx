"use client"

import * as React from "react"
import { CheckIcon, CopyIcon } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"

export type CodeRow = {
  id: string
  hint: string
  batch: string
  months: number
  status: string
  username: string
  message: string
  created: number
  updated: number
  progress: number
  folder: string
  copyable: boolean
  checkout_url?: string
  link_created?: number
  link_regenerated?: boolean
  link_state?: string
  account_id?: string
}

export type Folder = { id: string; name: string; count: number }

export type ListResponse = {
  codes: CodeRow[]
  payments_enabled: boolean
  page: number
  has_more: boolean
  folder: string
  folders: Folder[]
  stats: Record<string, number>
}

export type StatsResponse = {
  codes: Record<string, number>
  rates: { redeemed: number; success: number }
  months: { months: number; total: number; succeeded: number }[]
  daily: { date: string; created: number; redeemed: number; succeeded: number }[]
  review_stages: { progress: number; count: number }[]
}

export type AccountHealth = {
  ok: boolean
  fails: number
  checked_at: string
  latency_ms: number
  error?: string
}

export type AccountView = {
  id: string
  label: string
  enabled: boolean
  has_cookie: boolean
  proxy: { type: string; server: string; server_port: number; method: string }
  fingerprint: string
  health?: AccountHealth
}

export type AccountsResponse = {
  accounts: AccountView[]
  enabled: number
  records: { api_auth?: boolean; stripe_key?: boolean; catalog?: boolean }
}

export type ManualPlan = { months: number; amount: number; currency: string }

export const statusLabels: Record<string, string> = {
  active: "未使用",
  processing: "处理中",
  succeeded: "已完成",
  review: "待核实",
  revoked: "已停用",
}

// Link states are the payment side of one order, which the code status does not
// capture: an order can be "processing" with a link that already expired, or
// with one the issuer refused.
export const linkStateLabels: Record<string, { label: string; tone: "muted" | "active" | "warn" | "bad" | "good" }> = {
  waiting: { label: "待付款", tone: "active" },
  paid: { label: "已付款", tone: "good" },
  expired: { label: "已过期", tone: "warn" },
  declined: { label: "已拒绝", tone: "bad" },
}

export function linkStateOf(row: CodeRow) {
  if (row.status === "succeeded") return "paid"
  if (!row.checkout_url) return ""
  // The server derives expiry too; recomputing it here lets an open panel age a
  // link out without waiting for a refresh.
  const ttl = 3 * 60
  if (row.link_state === "waiting" && row.link_created && row.link_created + ttl < Date.now() / 1000) {
    return "expired"
  }
  return row.link_state ?? ""
}

export function linkExpiry(row: CodeRow) {
  if (!row.link_created) return 0
  return row.link_created + 3 * 60
}

export function statusVariant(status: string) {
  switch (status) {
    case "succeeded":
      return "default" as const
    case "processing":
      return "secondary" as const
    case "review":
      return "destructive" as const
    default:
      return "outline" as const
  }
}

export function CopyButton({
  value,
  label = "复制",
  disabled,
}: {
  value: string
  label?: string
  disabled?: boolean
}) {
  const [copied, setCopied] = React.useState(false)
  return (
    <Button
      type="button"
      variant="ghost"
      size="xs"
      disabled={disabled || !value}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value)
          setCopied(true)
          window.setTimeout(() => setCopied(false), 1500)
        } catch {
          setCopied(false)
        }
      }}
    >
      {copied ? <CheckIcon /> : <CopyIcon />}
      {copied ? "已复制" : label}
    </Button>
  )
}

export function InlineError({ children }: { children?: React.ReactNode }) {
  if (!children) return null
  return (
    <p role="alert" className="text-xs text-destructive">
      {children}
    </p>
  )
}

export function PanelStatus({ children }: { children?: React.ReactNode }) {
  if (!children) return null
  return (
    <p role="status" className="text-xs text-muted-foreground">
      {children}
    </p>
  )
}

export function useDebounced<T>(value: T, delay = 250) {
  const [debounced, setDebounced] = React.useState(value)
  React.useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), delay)
    return () => window.clearTimeout(timer)
  }, [value, delay])
  return debounced
}

export { Badge }
