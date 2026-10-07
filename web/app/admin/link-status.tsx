"use client"

import * as React from "react"
import { ExternalLinkIcon } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { buttonVariants } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { formatDate } from "@/lib/api"
import { cn } from "@/lib/utils"

import { CopyButton, linkExpiry, linkStateLabels, linkStateOf, type CodeRow } from "./shared"

const toneClass = {
  muted: "text-muted-foreground",
  active: "text-primary",
  good: "text-primary",
  warn: "text-amber-600 dark:text-amber-500",
  bad: "text-destructive",
} as const

function remaining(expiresAt: number, now: number) {
  const seconds = Math.max(0, Math.ceil(expiresAt - now / 1000))
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`
}

// A link's state is time-dependent, so the panel recomputes it locally every
// second. Without this a "待付款" badge would keep claiming there is time left
// long after the window closed.
function useClock(active: boolean) {
  const [now, setNow] = React.useState(() => Date.now())
  React.useEffect(() => {
    if (!active) return
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [active])
  return now
}

export function LinkCell({ row }: { row: CodeRow }) {
  const [open, setOpen] = React.useState(false)
  const state = linkStateOf(row)
  const expiresAt = linkExpiry(row)
  const now = useClock(state === "waiting")

  if (!state) {
    return <span className="text-xs text-muted-foreground">—</span>
  }
  const entry = linkStateLabels[state]
  const live = state === "waiting" && expiresAt * 1000 > now

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="flex items-baseline gap-1.5 text-xs underline-offset-4 hover:underline"
      >
        <span className={toneClass[entry.tone]}>{entry.label}</span>
        {live ? (
          <span className="font-mono text-muted-foreground tabular-nums">
            {remaining(expiresAt, now)}
          </span>
        ) : null}
      </button>
      <LinkDetail row={row} open={open} onOpenChange={setOpen} />
    </>
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[5.5rem_1fr] items-baseline gap-3">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="min-w-0 text-sm break-words">{children}</span>
    </div>
  )
}

function LinkDetail({
  row,
  open,
  onOpenChange,
}: {
  row: CodeRow
  open: boolean
  onOpenChange: (value: boolean) => void
}) {
  const state = linkStateOf(row)
  const expiresAt = linkExpiry(row)
  const now = useClock(open && state === "waiting")
  const entry = linkStateLabels[state]
  const live = state === "waiting" && expiresAt * 1000 > now

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-sm">尾号 {row.hint}</span>
            <Badge variant="outline" className={toneClass[entry?.tone ?? "muted"]}>
              {entry?.label ?? "未生成链接"}
            </Badge>
          </DialogTitle>
          <DialogDescription>
            {row.username ? `@${row.username} · ` : ""}
            {row.months} 个月
            {row.link_regenerated ? " · 已重新生成过一次" : ""}
          </DialogDescription>
        </DialogHeader>

        <div className="grid gap-3">
          {expiresAt ? (
            <Row label="链接有效期">
              <span className={cn("font-mono tabular-nums", !live && "text-muted-foreground")}>
                {live ? `剩余 ${remaining(expiresAt, now)}` : "已结束"}
              </span>
              <span className="ml-2 text-xs text-muted-foreground">
                至 {new Date(expiresAt * 1000).toLocaleTimeString("zh-CN")}
              </span>
            </Row>
          ) : null}
          <Row label="生成时间">{formatDate(row.link_created)}</Row>
          {row.account_id ? (
            <Row label="X 账号">
              <span className="font-mono text-xs">{row.account_id}</span>
            </Row>
          ) : null}
          <Row label="订单状态">{row.message || "—"}</Row>
          <Row label="关联账号">{row.username ? `@${row.username}` : "尚未绑定"}</Row>
        </div>

        {row.checkout_url ? (
          <div className="grid gap-2">
            <p className="rounded-lg border border-border bg-muted/40 p-3 font-mono text-xs break-all">
              {row.checkout_url}
            </p>
            <div className="flex flex-wrap gap-2">
              <a
                className={cn(buttonVariants({ variant: "outline", size: "sm" }))}
                href={row.checkout_url}
                target="_blank"
                rel="noreferrer noopener"
              >
                <ExternalLinkIcon />
                打开 Stripe 页面
              </a>
              <CopyButton value={row.checkout_url} label="复制链接" />
            </div>
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">
            这笔订单还没有生成付款链接。
            {row.status === "processing" ? "系统仍在处理，可以稍后刷新查看。" : ""}
          </p>
        )}
      </DialogContent>
    </Dialog>
  )
}
