// App-level handlers already publish failures to the global alert. Event
// boundaries still need to consume the rejected promise to avoid an
// unhandled-rejection console error.
export function runAction(action: Promise<unknown>): void {
  void action.catch(() => undefined)
}
