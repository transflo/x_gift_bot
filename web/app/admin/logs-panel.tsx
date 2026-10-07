"use client"

import * as React from "react"
import { DownloadIcon, Loader2Icon, RefreshCwIcon } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
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
import { adminApi, adminBase, adminHeader, formatBytes, formatTime } from "@/lib/api"
import { cn } from "@/lib/utils"

import { InlineError } from "./shared"

type Entry = {
  t: number
  kind: string
  level?: string
  msg?: string
  ip?: string
  method?: string
  path?: string
  status?: number
  ms?: number
  ua?: string
  referer?: string
  extra?: Record<string, unknown>
}

type LogFile = { name: string; bytes: number; modified: number; active: boolean }

type LogsResponse = { entries: Entry[]; files: LogFile[] }

const LEVELS = [
  { value: "all", label: "全部" },
  { value: "info", label: "普通" },
  { value: "warn", label: "警告" },
  { value: "error", label: "错误" },
]

const KIND_LABELS: Record<string, string> = {
  http: "访问",
  log: "进程",
  order: "订单",
  admin: "管理",
}

function levelTone(level?: string) {
  switch (level) {
    case "error":
      return "text-destructive"
    case "warn":
      return "text-amber-600 dark:text-amber-500"
    default:
      return "text-muted-foreground"
  }
}

function statusTone(status?: number) {
  if (!status) return "text-muted-foreground"
  if (status >= 500) return "text-destructive"
  if (status >= 400) return "text-amber-600 dark:text-amber-500"
  return "text-muted-foreground"
}

export function LogsPanel({ password }: { password: string }) {
  const [data, setData] = React.useState<LogsResponse | null>(null)
  const [level, setLevel] = React.useState("all")
  const [live, setLive] = React.useState(true)
  const [loading, setLoading] = React.useState(false)
  const [error, setError] = React.useState("")
  const [detail, setDetail] = React.useState<Entry | null>(null)

  const load = React.useCallback(async () => {
    setLoading(true)
    const query = new URLSearchParams({ lines: "300" })
    if (level !== "all") query.set("level", level)
    const result = await adminApi<LogsResponse>(password, `/api/admin/logs?${query.toString()}`)
    setLoading(false)
    if (!result.ok) {
      setError(result.message || "无法读取日志。")
      return
    }
    setError("")
    setData(result.data)
  }, [password, level])

  React.useEffect(() => {
    void load()
  }, [load])

  React.useEffect(() => {
    if (!live) return
    const timer = window.setInterval(() => void load(), 5000)
    return () => window.clearInterval(timer)
  }, [live, load])

  // The download is fetched rather than linked: an <a href> cannot carry the
  // Basic credentials the admin API requires, so the file arrives as a blob.
  async function download(name: string) {
    setError("")
    try {
      const response = await fetch(`${adminBase()}/api/admin/logs/download?file=${encodeURIComponent(name)}`, {
        headers: adminHeader(password),
        credentials: "same-origin",
      })
      if (!response.ok) {
        setError("无法下载日志文件。")
        return
      }
      const blob = await response.blob()
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement("a")
      anchor.href = url
      anchor.download = name.endsWith(".log") ? "xgift.log" : `${name.replace(/\.log\./, "-")}.log`
      anchor.click()
      URL.revokeObjectURL(url)
    } catch {
      setError("无法下载日志文件。")
    }
  }

  const entries = data?.entries ?? []
  const files = data?.files ?? []

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <div className="grid gap-1">
          <Label className="text-xs">级别</Label>
          <Select value={level} onValueChange={(value) => setLevel(value as string)}>
            <SelectTrigger className="w-32" aria-label="日志级别">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {LEVELS.map((item) => (
                <SelectItem key={item.value} value={item.value}>
                  {item.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <Button variant="outline" className="mt-5" onClick={() => void load()} disabled={loading}>
          {loading ? <Loader2Icon className="animate-spin" /> : <RefreshCwIcon />}
          刷新
        </Button>
        <div className="mt-5 flex items-center gap-2 text-sm">
          <Switch checked={live} aria-label="自动刷新" onCheckedChange={setLive} />
          每 5 秒自动刷新
        </div>
        <div className="ml-auto mt-5 flex flex-wrap gap-2">
          {files.map((file) => (
            <Button
              key={file.name}
              variant="outline"
              size="sm"
              onClick={() => void download(file.name)}
              title={file.active ? "当前日志文件" : "已归档的日志文件"}
            >
              <DownloadIcon />
              {file.name.replace(/^xgift\.log/, "日志")}
              <span className="text-xs text-muted-foreground">{formatBytes(file.bytes)}</span>
            </Button>
          ))}
        </div>
      </div>

      <InlineError>{error}</InlineError>

      <Card className="overflow-hidden p-0">
        <div className="max-h-[65svh] overflow-auto">
          <Table>
            <TableHeader className="sticky top-0 z-10 bg-card">
              <TableRow>
                <TableHead className="w-40">时间</TableHead>
                <TableHead className="w-20">类型</TableHead>
                <TableHead className="w-36">来源 IP</TableHead>
                <TableHead className="w-24">请求</TableHead>
                <TableHead>内容</TableHead>
                <TableHead className="w-16 text-right">状态</TableHead>
                <TableHead className="w-16 text-right">耗时</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {entries.map((entry, index) => (
                <TableRow
                  key={`${entry.t}-${index}`}
                  className="cursor-pointer"
                  onClick={() => setDetail(entry)}
                >
                  <TableCell className="font-mono text-xs whitespace-nowrap text-muted-foreground">
                    {formatTime(entry.t)}
                  </TableCell>
                  <TableCell className="text-xs">{KIND_LABELS[entry.kind] ?? entry.kind}</TableCell>
                  <TableCell className="font-mono text-xs">
                    {entry.ip ? (
                      entry.ip
                    ) : (
                      <span className="text-muted-foreground">—</span>
                    )}
                  </TableCell>
                  <TableCell className="font-mono text-xs">{entry.method ?? "—"}</TableCell>
                  <TableCell className={cn("max-w-md truncate text-xs", levelTone(entry.level))}>
                    {entry.path ?? entry.msg ?? "—"}
                  </TableCell>
                  <TableCell className={cn("text-right text-xs tabular-nums", statusTone(entry.status))}>
                    {entry.status ?? "—"}
                  </TableCell>
                  <TableCell className="text-right text-xs tabular-nums text-muted-foreground">
                    {entry.ms != null ? `${entry.ms} ms` : "—"}
                  </TableCell>
                </TableRow>
              ))}
              {entries.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={7} className="py-10 text-center text-sm text-muted-foreground">
                    还没有日志。访问首页或兑换页之后这里会出现记录。
                  </TableCell>
                </TableRow>
              ) : null}
            </TableBody>
          </Table>
        </div>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">关于这些日志</CardTitle>
          <CardDescription>
            单文件最大 8 MB，保留 4 个归档，日志总量不会超过 32 MB。存活探针与静态资源不记录。
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-1 text-xs text-muted-foreground">
          <p>
            来源 IP 取自反向代理的 <span className="font-mono">X-Real-IP</span>（其次
            <span className="font-mono"> X-Forwarded-For</span>）。反代没有传递这些头时会退回到连接来源，
            在容器里那会是代理的内网地址而不是访客的真实地址。
          </p>
          <p>请求 URL 中的查询参数不会写入日志，因此下载的文件可以直接交给他人查看。</p>
        </CardContent>
      </Card>

      <Dialog open={detail !== null} onOpenChange={(value) => (!value ? setDetail(null) : undefined)}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>日志详情</DialogTitle>
            <DialogDescription>{detail ? formatTime(detail.t) : ""}</DialogDescription>
          </DialogHeader>
          {detail ? (
            <div className="grid gap-2 text-sm">
              {[
                ["类型", KIND_LABELS[detail.kind] ?? detail.kind],
                ["级别", detail.level ?? "—"],
                ["来源 IP", detail.ip ?? "—"],
                ["请求", detail.method && detail.path ? `${detail.method} ${detail.path}` : "—"],
                ["状态码", detail.status ? String(detail.status) : "—"],
                ["耗时", detail.ms != null ? `${detail.ms} ms` : "—"],
                ["内容", detail.msg ?? "—"],
                ["Referer", detail.referer || "—"],
              ].map(([label, value]) => (
                <div key={label} className="grid grid-cols-[5rem_1fr] gap-3">
                  <span className="text-xs text-muted-foreground">{label}</span>
                  <span className="break-all">{value}</span>
                </div>
              ))}
              <div className="grid grid-cols-[5rem_1fr] gap-3">
                <span className="text-xs text-muted-foreground">User-Agent</span>
                <span className="break-all text-xs">{detail.ua || "—"}</span>
              </div>
              {detail.extra ? (
                <div className="grid grid-cols-[5rem_1fr] gap-3">
                  <span className="text-xs text-muted-foreground">附加</span>
                  <pre className="overflow-x-auto rounded-md border border-border bg-muted/40 p-2 font-mono text-xs">
                    {JSON.stringify(detail.extra, null, 2)}
                  </pre>
                </div>
              ) : null}
            </div>
          ) : null}
        </DialogContent>
      </Dialog>
    </div>
  )
}
