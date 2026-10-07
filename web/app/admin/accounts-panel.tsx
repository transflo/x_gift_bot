"use client"

import * as React from "react"
import {
  CheckCircle2Icon,
  Loader2Icon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  ShieldCheckIcon,
  Trash2Icon,
  XCircleIcon,
} from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
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
import { adminApi } from "@/lib/api"

import { InlineError, PanelStatus, type AccountHealth, type AccountView, type AccountsResponse } from "./shared"

type Draft = {
  id: string
  label: string
  authToken: string
  ct0: string
  enabled: boolean
  server: string
  port: string
  method: string
  password: string
}

const emptyDraft: Draft = {
  id: "",
  label: "",
  authToken: "",
  ct0: "",
  enabled: true,
  server: "",
  port: "8388",
  method: "aes-256-gcm",
  password: "",
}

const methods = [
  "aes-256-gcm",
  "aes-192-gcm",
  "aes-128-gcm",
  "chacha20-ietf-poly1305",
  "xchacha20-ietf-poly1305",
  "2022-blake3-aes-128-gcm",
  "2022-blake3-aes-256-gcm",
  "2022-blake3-chacha20-poly1305",
  "aes-256-cfb",
  "aes-128-cfb",
  "none",
]

export function AccountsPanel({ password }: { password: string }) {
  const [data, setData] = React.useState<AccountsResponse | null>(null)
  const [loading, setLoading] = React.useState(false)
  const [error, setError] = React.useState("")
  const [notice, setNotice] = React.useState("")
  const [testing, setTesting] = React.useState<string | null>(null)
  const [draft, setDraft] = React.useState<Draft | null>(null)
  const [saving, setSaving] = React.useState(false)
  const [deleteTarget, setDeleteTarget] = React.useState<AccountView | null>(null)
  const [deleteError, setDeleteError] = React.useState("")

  const load = React.useCallback(async () => {
    setLoading(true)
    setError("")
    const result = await adminApi<AccountsResponse>(password, "/api/admin/accounts")
    setLoading(false)
    if (!result.ok) {
      setError(result.message || "无法读取账号池。")
      return
    }
    setData(result.data)
  }, [password])

  React.useEffect(() => {
    void load()
  }, [load])

  const records = data?.records ?? {}

  async function save(event: React.FormEvent) {
    event.preventDefault()
    if (!draft) return
    setSaving(true)
    setError("")
    const result = await adminApi<{ message: string }>(password, "/api/admin/accounts", {
      id: draft.id,
      label: draft.label,
      auth_token: draft.authToken,
      ct0: draft.ct0,
      enabled: draft.enabled,
      proxy: {
        type: "shadowsocks",
        server: draft.server,
        server_port: Number(draft.port),
        method: draft.method,
        password: draft.password,
      },
    })
    setSaving(false)
    if (!result.ok) {
      setError(result.message || "保存失败。")
      return
    }
    setDraft(null)
    setNotice(draft.id ? "账号已更新。" : "账号已添加。")
    void load()
  }

  async function toggle(account: AccountView, enabled: boolean) {
    const result = await adminApi<{ message: string }>(password, "/api/admin/accounts/toggle", {
      id: account.id,
      enabled,
    })
    if (!result.ok) {
      setError(result.message || "更新失败。")
      return
    }
    void load()
  }

  async function test(account: AccountView) {
    setTesting(account.id)
    setError("")
    setNotice("")
    const result = await adminApi<{ message: string; latency_ms?: number }>(
      password,
      "/api/admin/accounts/test",
      { id: account.id },
    )
    setTesting(null)
    if (!result.ok) {
      setError(result.message || "连通性检查失败。")
      return
    }
    setNotice(`${account.label || account.id}：${result.data.message}${result.data.latency_ms ? `（${result.data.latency_ms} ms）` : ""}`)
  }

  async function remove() {
    if (!deleteTarget) return
    setDeleteError("")
    const result = await adminApi<{ message: string }>(password, "/api/admin/accounts/delete", {
      id: deleteTarget.id,
    })
    if (!result.ok) {
      setDeleteError(result.message || "删除失败。")
      return
    }
    setDeleteTarget(null)
    setNotice("账号已删除。")
    void load()
  }

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <Button onClick={() => setDraft({ ...emptyDraft })}>
          <PlusIcon />
          添加 X 账号
        </Button>
        <Button variant="outline" onClick={() => void load()} disabled={loading}>
          {loading ? <Loader2Icon className="animate-spin" /> : <RefreshCwIcon />}
          刷新
        </Button>
        <div className="ml-auto flex flex-wrap items-center gap-2 text-xs">
          <Badge variant={records.api_auth ? "secondary" : "destructive"}>
            API 授权 {records.api_auth ? "已配置" : "缺失"}
          </Badge>
          <Badge variant={records.stripe_key ? "secondary" : "destructive"}>
            Stripe 公钥 {records.stripe_key ? "已配置" : "缺失"}
          </Badge>
          <Badge variant={records.catalog ? "secondary" : "destructive"}>
            套餐目录 {records.catalog ? "已配置" : "缺失"}
          </Badge>
        </div>
      </div>

      <InlineError>{error}</InlineError>
      <PanelStatus>{notice}</PanelStatus>

      <Card className="p-0">
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>名称</TableHead>
                <TableHead>代理</TableHead>
                <TableHead>测活</TableHead>
                <TableHead>Cookie</TableHead>
                <TableHead>启用</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(data?.accounts ?? []).map((account) => (
                <TableRow key={account.id}>
                  <TableCell>
                    <p className="font-medium">{account.label || "未命名"}</p>
                    <p className="font-mono text-xs text-muted-foreground">{account.id}</p>
                  </TableCell>
                  <TableCell className="text-sm">
                    <p className="font-mono text-xs">
                      {account.proxy.server}:{account.proxy.server_port}
                    </p>
                    <p className="text-xs text-muted-foreground">
                      Shadowsocks · {account.proxy.method}
                    </p>
                  </TableCell>
                  <TableCell>
                    <HealthCell health={account.health} />
                  </TableCell>
                  <TableCell>
                    {account.has_cookie ? (
                      <span className="flex items-center gap-1 text-xs text-muted-foreground">
                        <ShieldCheckIcon className="size-3.5" />
                        已加密保存
                      </span>
                    ) : (
                      <span className="flex items-center gap-1 text-xs text-destructive">
                        <XCircleIcon className="size-3.5" />
                        缺失
                      </span>
                    )}
                  </TableCell>
                  <TableCell>
                    <Switch
                      checked={account.enabled}
                      aria-label={`${account.label || account.id} 启用状态`}
                      onCheckedChange={(value) => void toggle(account, value)}
                    />
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="xs"
                        onClick={() => void test(account)}
                        disabled={testing !== null}
                      >
                        {testing === account.id ? <Loader2Icon className="animate-spin" /> : <CheckCircle2Icon />}
                        测试
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon-xs"
                        aria-label="编辑"
                        onClick={() =>
                          setDraft({
                            id: account.id,
                            label: account.label,
                            authToken: "",
                            ct0: "",
                            enabled: account.enabled,
                            server: account.proxy.server,
                            port: String(account.proxy.server_port),
                            method: account.proxy.method,
                            password: "",
                          })
                        }
                      >
                        <PencilIcon />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon-xs"
                        aria-label="删除"
                        onClick={() => {
                          setDeleteError("")
                          setDeleteTarget(account)
                        }}
                      >
                        <Trash2Icon />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
              {data && data.accounts.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={5} className="py-10 text-center text-sm text-muted-foreground">
                    账号池为空。每个账号需要一个 X Cookie 和一个 Shadowsocks 代理。
                  </TableCell>
                </TableRow>
              ) : null}
            </TableBody>
          </Table>
        </div>
      </Card>

      <p className="text-xs leading-relaxed text-muted-foreground">
        每个 X 账号绑定一个 Shadowsocks 代理，系统只支持 Shadowsocks。账号与代理凭据均以
        AES-256-GCM 加密保存在保管库中，页面不会回显明文。
      </p>

      <Dialog open={draft !== null} onOpenChange={(value) => (!value ? setDraft(null) : undefined)}>
        <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{draft?.id ? "编辑 X 账号" : "添加 X 账号"}</DialogTitle>
            <DialogDescription>
              {draft?.id ? "留空 Cookie 与代理密码表示保持原值。" : "Cookie 在 x.com 登录后于开发者工具中获取。"}
            </DialogDescription>
          </DialogHeader>
          {draft ? (
            <form className="grid gap-4" onSubmit={save}>
              <div className="grid gap-2">
                <Label htmlFor="label">账号名称</Label>
                <Input
                  id="label"
                  value={draft.label}
                  maxLength={64}
                  onChange={(event) => setDraft({ ...draft, label: event.target.value })}
                  placeholder="例如：主账号"
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="auth">auth_token</Label>
                <Input
                  id="auth"
                  type="password"
                  autoComplete="off"
                  value={draft.authToken}
                  onChange={(event) => setDraft({ ...draft, authToken: event.target.value })}
                  placeholder={draft.id ? "留空保持原值" : ""}
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="ct0">ct0</Label>
                <Input
                  id="ct0"
                  type="password"
                  autoComplete="off"
                  value={draft.ct0}
                  onChange={(event) => setDraft({ ...draft, ct0: event.target.value })}
                  placeholder={draft.id ? "留空保持原值" : ""}
                />
              </div>
              <div className="rounded-xl border border-border p-3">
                <p className="mb-3 text-sm font-medium">Shadowsocks 代理</p>
                <div className="grid gap-3">
                  <div className="grid gap-2">
                    <Label htmlFor="server">服务器地址</Label>
                    <Input
                      id="server"
                      value={draft.server}
                      onChange={(event) => setDraft({ ...draft, server: event.target.value })}
                      placeholder="IP 或域名"
                    />
                  </div>
                  <div className="grid grid-cols-2 gap-3">
                    <div className="grid gap-2">
                      <Label htmlFor="port">端口</Label>
                      <Input
                        id="port"
                        type="number"
                        min={1}
                        max={65535}
                        value={draft.port}
                        onChange={(event) => setDraft({ ...draft, port: event.target.value })}
                      />
                    </div>
                    <div className="grid gap-2">
                      <Label>加密方式</Label>
                      <Select
                        value={draft.method}
                        onValueChange={(value) => setDraft({ ...draft, method: value as string })}
                      >
                        <SelectTrigger className="w-full">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {methods.map((method) => (
                            <SelectItem key={method} value={method}>
                              {method}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="proxy-password">代理密码</Label>
                    <Input
                      id="proxy-password"
                      type="password"
                      autoComplete="off"
                      value={draft.password}
                      onChange={(event) => setDraft({ ...draft, password: event.target.value })}
                      placeholder={draft.id ? "留空保持原值" : ""}
                    />
                  </div>
                </div>
              </div>
              <div className="flex items-center justify-between rounded-xl border border-border p-3">
                <div>
                  <p className="text-sm font-medium">启用账号</p>
                  <p className="text-xs text-muted-foreground">停用后不再参与链接生成与负载均衡。</p>
                </div>
                <Switch
                  checked={draft.enabled}
                  aria-label="启用账号"
                  onCheckedChange={(value) => setDraft({ ...draft, enabled: value })}
                />
              </div>
              <DialogFooter>
                <Button type="button" variant="outline" onClick={() => setDraft(null)}>
                  取消
                </Button>
                <Button type="submit" disabled={saving}>
                  {saving ? <Loader2Icon className="animate-spin" /> : null}
                  保存
                </Button>
              </DialogFooter>
            </form>
          ) : null}
        </DialogContent>
      </Dialog>

      <Dialog
        open={deleteTarget !== null}
        onOpenChange={(value) => (!value ? setDeleteTarget(null) : undefined)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>删除 X 账号</DialogTitle>
            <DialogDescription>
              确定删除「{deleteTarget?.label || deleteTarget?.id}」吗？正在使用该账号的付款链接不受影响，但后续订单会使用其他账号。
            </DialogDescription>
          </DialogHeader>
          <InlineError>{deleteError}</InlineError>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteTarget(null)}>
              取消
            </Button>
            <Button variant="destructive" onClick={() => void remove()}>
              删除
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

// Health is probed in the background every few minutes; an account that keeps
// failing is skipped when a new order is assigned, so the operator only needs
// this column to explain why a proxy stopped being used.
function HealthCell({ health }: { health?: AccountHealth }) {
  if (!health) {
    return <span className="text-xs text-muted-foreground">未测活</span>
  }
  if (health.ok) {
    return (
      <span className="flex items-center gap-1 text-xs text-muted-foreground">
        <CheckCircle2Icon className="size-3.5" />
        正常
      </span>
    )
  }
  return (
    <span className="flex items-center gap-1 text-xs text-destructive" title={health.error}>
      <XCircleIcon className="size-3.5" />
      连续失败 {health.fails} 次
    </span>
  )
}
