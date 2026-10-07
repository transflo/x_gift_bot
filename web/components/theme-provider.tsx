"use client"

import * as React from "react"
import { ThemeProvider as NextThemesProvider, useTheme } from "next-themes"

// Every response carries a fresh CSP nonce. next-themes injects a <style> element
// when it swaps themes, and without the nonce that element is blocked and logged
// as an error on every page. The value is read straight out of the document
// rather than passed down, because the pages are a static export and nothing on
// the server can reach these components.
//
// Read at module scope, not in an effect: effects run child-first, so
// next-themes would already have injected its element with an empty nonce.
// next-themes only applies this value to elements it creates on the client — its
// server-rendered script deliberately gets an empty one — so there is nothing
// for hydration to disagree about.
const CSP_NONCE =
  typeof document === "undefined"
    ? undefined
    : // The IDL property still returns the value; getAttribute does not, since
      // browsers hide nonces to keep them out of injected markup.
      document.querySelector<HTMLScriptElement>("script[nonce]")?.nonce || undefined

function ThemeProvider({
  children,
  ...props
}: React.ComponentProps<typeof NextThemesProvider>) {
  return (
    <NextThemesProvider
      attribute="class"
      defaultTheme="system"
      enableSystem
      disableTransitionOnChange
      nonce={CSP_NONCE}
      {...props}
    >
      <ThemeHotkey />
      {children}
    </NextThemesProvider>
  )
}

function isTypingTarget(target: EventTarget | null) {
  if (!(target instanceof HTMLElement)) {
    return false
  }

  return (
    target.isContentEditable ||
    target.tagName === "INPUT" ||
    target.tagName === "TEXTAREA" ||
    target.tagName === "SELECT"
  )
}

function ThemeHotkey() {
  const { resolvedTheme, setTheme } = useTheme()

  React.useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.defaultPrevented || event.repeat) {
        return
      }

      if (event.metaKey || event.ctrlKey || event.altKey) {
        return
      }

      if (event.key.toLowerCase() !== "d") {
        return
      }

      if (isTypingTarget(event.target)) {
        return
      }

      setTheme(resolvedTheme === "dark" ? "light" : "dark")
    }

    window.addEventListener("keydown", onKeyDown)

    return () => {
      window.removeEventListener("keydown", onKeyDown)
    }
  }, [resolvedTheme, setTheme])

  return null
}

export { ThemeProvider }
