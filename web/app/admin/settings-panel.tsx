"use client"

import * as React from "react"
import {
  CheckCircle2Icon,
  HardDriveIcon,
  Loader2Icon,
  MegaphoneIcon,
  RefreshCwIcon,
  Trash2Icon,
} from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { Textarea } from "@/components/ui/textarea"
import { adminApi, formatAmount, formatBytes, formatTime } from "@/lib/api"
import { cn } from "@/lib/utils"

import { InlineError, PanelStatus, CopyButton } from "./shared"

type SettingsResponse = {
  stripe_key: { set: boolean; key: string; source: string; invalid?: string }
  catalog: {
    available: boolean
    merchant: string
    currency: string
    plans: { months: number; amount: number; product: string }[]
    error?: string
  }
  payments_enabled: boolean
  announcement: { enabled: boolean; text: string; level: string; updated: number }
  storage: {
    free_bytes: number
    total_bytes: number
    files: { name: string; bytes: number }[]
    log_bytes: number
    log_files: { name: string; bytes: number; active: boolean }[]
    records: {
      prefix: string
      label: string
      records: number
      bytes: number
      pruned: boolean
      days?: number
    }[]
    records_bytes: number
  }
  retention: {
    last_run: number
    last: { at: number; removed: number; vacuumed: boolean; error?: string }
    rules: { prefix: string; label: string; days: number }[]
  }
}

const LEVELS = [
  { value: "info", label: "普通" },
  { value: "warning", label: "提醒" },
  { value: "critical", label: "重要" },
]

const emptyAnnouncement = { enabled: false, text: "", level: "info", updated: 0 }

export function SettingsPanel({ password }: { password: string }) {
  const [data, setData] = React.useState<SettingsResponse | null>(null)
  const [stripeKey, setStripeKey] = React.useState("")
  const [announcement, setAnnouncement] = React.useState(emptyAnnouncement)
  const [busy, setBusy] = React.useState<"stripe" | "announcement" | "maintenance" | null>(null)
  const [error, setError] = React.useState("")
  const [notice, setNotice] = React.useState("")

  const load = React.useCallback(async () => {
    const result = await adminApi<SettingsResponse>(password, "/api/admin/settings")
    if (!result.ok) {
      setError(result.message || "无法读取设置。")
      return
    }
    setData(result.data)
    setAnnouncement(result.data.announcement ?? emptyAnnouncement)
  }, [password])

  React.useEffect(() => {
    void load()
  }, [load])

  async function saveStripeKey(event: React.FormEvent) {
    event.preventDefault()
    setBusy("stripe")
    setError("")
    setNotice("")
    const result = await adminApi<{ message: string }>(password, "/api/admin/settings/stripe-key", {
      key: stripeKey.trim(),
    })
    setBusy(null)
    if (!result.ok) {
      setError(result.message || "保存失败。")
      return
    }
    // The key is a credential: do not leave it in a form field longer than the
    // save takes.
    setStripeKey("")
    setNotice(result.data.message || "已保存。")
    void load()
  }

  async function saveAnnouncement(event: React.FormEvent) {
    event.preventDefault()
    setBusy("announcement")
    setError("")
    setNotice("")
    const result = await adminApi<{ message: string }>(password, "/api/admin/settings/announcement", {
      enabled: announcement.enabled,
      text: announcement.text,
      level: announcement.level,
    })
    setBusy(null)
    if (!result.ok) {
      setError(result.message || "保存失败。")
      return
    }
    setNotice(result.data.message || "已保存。")
    void load()
  }

  async function maintain() {
    setBusy("maintenance")
    setError("")
    setNotice("")
    // The empty object is load-bearing: api() derives the method from whether a
    // body was passed, so omitting it would send a GET to a POST-only route and
    // land on the static handler's 404 instead of running the cleanup.
    const result = await adminApi<{ message: string; result: { removed: number } }>(
      password,
      "/api/admin/settings/maintenance",
      {},
    )
    setBusy(null)
    if (!result.ok) {
      setError(result.message || "清理失败。")
      return
    }
    setNotice(`清理完成，移除 ${result.data.result.removed} 条过期记录。`)
    void load()
  }

  const storage = data?.storage
  const used = storage ? storage.total_bytes - storage.free_bytes : 0
  const usedPercent = storage?.total_bytes ? (used / storage.total_bytes) * 100 : 0
  const files = [
    ...(storage?.files ?? []).map((f) => ({ name: f.name, bytes: f.bytes, note: "数据库" })),
    ...(storage?.log_files ?? []).map((f) => ({
      name: f.name,
      bytes: f.bytes,
      note: f.active ? "日志（当前）" : "日志（归档）",
    })),
  ]

  return (
    <div className="grid gap-4">
      <div className="flex items-center justify-between">
        <p className="text-sm text-muted-foreground">站点配置与磁盘占用。</p>
        <Button variant="outline" size="sm" onClick={() => void load()}>
          <RefreshCwIcon />
          刷新
        </Button>
      </div>
      <InlineError>{error}</InlineError>
      <PanelStatus>{notice}</PanelStatus>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Stripe 公钥</CardTitle>
          <CardDescription>
            X 结账页使用的 <code className="font-mono">pk_live_</code> publishable key，用于创建付款链接后只读核验金额与收款方。
            环境变量 <code className="font-mono">XGIFT_STRIPE_KEY</code> 只在记录为空时生效，之后改这里即可。
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-3">
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant={data?.stripe_key.set ? "secondary" : "destructive"}>
              {data?.stripe_key.set ? `已配置（来源：${data.stripe_key.source}）` : "未配置"}
            </Badge>
            {data?.stripe_key.invalid ? (
              <Badge variant="destructive">{data.stripe_key.invalid}</Badge>
            ) : null}
          </div>
          {data?.stripe_key.set ? (
            <div className="flex flex-wrap items-center gap-2">
              <code className="min-w-0 flex-1 rounded-md border border-border bg-muted/40 px-3 py-2 font-mono text-xs break-all">
                {data.stripe_key.key}
              </code>
              <CopyButton value={data.stripe_key.key} label="复制" />
            </div>
          ) : null}
          <form className="flex flex-col gap-2 sm:flex-row" onSubmit={saveStripeKey}>
            <Input
              value={stripeKey}
              onChange={(event) => setStripeKey(event.target.value)}
              placeholder={data?.stripe_key.set ? "填写新的公钥以替换" : "pk_live_..."}
              autoComplete="off"
              spellCheck={false}
              className="font-mono text-xs"
              aria-label="Stripe 公钥"
            />
            <Button type="submit" disabled={busy !== null || !stripeKey.trim()}>
              {busy === "stripe" ? <Loader2Icon className="animate-spin" /> : null}
              {data?.stripe_key.set ? "替换" : "保存"}
            </Button>
          </form>
          {data?.catalog.available ? (
            <div className="grid gap-1 rounded-lg border border-border p-3 text-xs text-muted-foreground">
              <p>
                商户 <span className="font-mono">{data.catalog.merchant}</span> · 币种 {data.catalog.currency}
              </p>
              {data.catalog.plans.map((plan) => (
                <p key={plan.months}>
                  {plan.months} 个月 · {formatAmount(plan.amount, data.catalog.currency)} ·{" "}
                  <span className="font-mono">{plan.product}</span>
                </p>
              ))}
            </div>
          ) : (
            <p className="text-xs text-destructive">{data?.catalog.error ?? "套餐目录不可用。"}</p>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <MegaphoneIcon className="size-4" />
            站点公告
          </CardTitle>
          <CardDescription>开启后会在首页和兑换页顶部显示一条横幅，保存后立即生效，无需重新部署。</CardDescription>
        </CardHeader>
        <CardContent>
          <form className="grid gap-3" onSubmit={saveAnnouncement}>
            <div className="flex items-center justify-between rounded-xl border border-border p-3">
              <div>
                <p className="text-sm font-medium">显示公告</p>
                <p className="text-xs text-muted-foreground">关闭后横幅立即消失，内容会保留。</p>
              </div>
              <Switch
                checked={announcement.enabled}
                aria-label="显示公告"
                onCheckedChange={(value) => setAnnouncement({ ...announcement, enabled: value })}
              />
            </div>
            <div className="grid gap-2">
              <Label>语气</Label>
              <Select
                value={announcement.level}
                onValueChange={(value) => setAnnouncement({ ...announcement, level: value as string })}
              >
                <SelectTrigger className="w-full sm:w-48">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {LEVELS.map((level) => (
                    <SelectItem key={level.value} value={level.value}>
                      {level.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-2">
              <Label htmlFor="announcement">内容</Label>
              <Textarea
                id="announcement"
                value={announcement.text}
                maxLength={200}
                rows={2}
                placeholder="例如：本周六 02:00–04:00 系统维护，兑换会暂时暂停。"
                onChange={(event) => setAnnouncement({ ...announcement, text: event.target.value })}
              />
              <p className="text-xs text-muted-foreground">
                单行文本，最多 200 字。当前 {Array.from(announcement.text).length} 字。
              </p>
            </div>
            {announcement.enabled && announcement.text.trim() ? (
              <div
                className={cn(
                  "flex items-start gap-2.5 rounded-lg px-4 py-2.5 text-sm",
                  announcement.level === "critical"
                    ? "bg-destructive text-white"
                    : "bg-foreground text-background",
                )}
              >
                <MegaphoneIcon className="mt-0.5 size-4 shrink-0 opacity-80" />
                <p>{announcement.text}</p>
              </div>
            ) : null}
            <div>
              <Button type="submit" disabled={busy !== null}>
                {busy === "announcement" ? <Loader2Icon className="animate-spin" /> : null}
                保存公告
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <HardDriveIcon className="size-4" />
            存储占用
          </CardTitle>
          <CardDescription>
            记录库与日志都会长期增长；系统每天自动清理一次过期记录，日志按大小滚动。
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4">
          {storage ? (
            <>
              <div>
                <div className="flex items-baseline justify-between text-sm">
                  <span>
                    所在分区已用 {formatBytes(used)} / {formatBytes(storage.total_bytes)}
                  </span>
                  <span className="text-xs text-muted-foreground">
                    剩余 {formatBytes(storage.free_bytes)}
                  </span>
                </div>
                <div className="mt-2 h-2 w-full overflow-hidden rounded-full bg-muted">
                  <div
                    className={cn(
                      "h-full rounded-full",
                      usedPercent > 90 ? "bg-destructive" : usedPercent > 75 ? "bg-amber-500" : "bg-primary",
                    )}
                    style={{ width: `${Math.min(100, usedPercent)}%` }}
                  />
                </div>
              </div>

              <div className="overflow-x-auto">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>文件</TableHead>
                      <TableHead>用途</TableHead>
                      <TableHead className="text-right">占用</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {files.map((file) => (
                      <TableRow key={file.name}>
                        <TableCell className="font-mono text-xs">{file.name}</TableCell>
                        <TableCell className="text-xs text-muted-foreground">{file.note}</TableCell>
                        <TableCell className="text-right tabular-nums">{formatBytes(file.bytes)}</TableCell>
                      </TableRow>
                    ))}
                    {files.length === 0 ? (
                      <TableRow>
                        <TableCell colSpan={3} className="py-6 text-center text-sm text-muted-foreground">
                          暂无文件。
                        </TableCell>
                      </TableRow>
                    ) : null}
                  </TableBody>
                </Table>
              </div>

              <div className="overflow-x-auto">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>记录</TableHead>
                      <TableHead className="text-right">条数</TableHead>
                      <TableHead className="text-right">占用</TableHead>
                      <TableHead>保留策略</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {storage.records.map((row) => (
                      <TableRow key={row.prefix}>
                        <TableCell className="text-sm">
                          {row.label}
                          <span className="ml-2 font-mono text-xs text-muted-foreground">{row.prefix}</span>
                        </TableCell>
                        <TableCell className="text-right tabular-nums">{row.records}</TableCell>
                        <TableCell className="text-right tabular-nums">{formatBytes(row.bytes)}</TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {row.pruned ? `${row.days} 天后清理` : "永久保留"}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>

              <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-border p-3">
                <div className="text-xs text-muted-foreground">
                  {data.retention.last_run ? (
                    <>
                      上次清理 {formatTime(data.retention.last_run)}，移除 {data.retention.last.removed} 条
                      {data.retention.last.vacuumed ? "，已回收空间" : ""}。
                    </>
                  ) : (
                    <>本次启动尚未清理；系统会在启动 5 分钟后执行第一次。</>
                  )}
                  {data.retention.last.error ? (
                    <span className="ml-1 text-destructive">{data.retention.last.error}</span>
                  ) : null}
                </div>
                <Button variant="outline" size="sm" onClick={() => void maintain()} disabled={busy !== null}>
                  {busy === "maintenance" ? (
                    <Loader2Icon className="animate-spin" />
                  ) : (
                    <Trash2Icon />
                  )}
                  立即清理
                </Button>
              </div>

              {data.payments_enabled ? (
                <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <CheckCircle2Icon className="size-3.5" />
                  充值通道已就绪。
                </p>
              ) : (
                <p className="text-xs text-muted-foreground">
                  充值通道未就绪：需要至少一个启用的 X 账号。
                </p>
              )}
            </>
          ) : (
            <p className="text-sm text-muted-foreground">正在读取存储信息…</p>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
