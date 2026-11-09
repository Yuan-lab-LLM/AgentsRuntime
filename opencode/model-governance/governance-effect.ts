import { Effect } from "effect"
import { governance, type Governance, type Task } from "./clawmanager-governance.mjs"

// raceFirst interrupts AND joins the losing fiber. The runtime callback then
// joins tool fibers; the caller sends its receipt only after this scope exits.
export function guardTask<A, E, R, R2>(
  operation: Effect.Effect<A, E, R>, task: Task | undefined,
  onFailure: Effect.Effect<void, never, R2>, client: Governance = governance,
) {
  if (!task) return operation
  const watch = Effect.forever(Effect.promise(() => client.check(task)).pipe(Effect.andThen(Effect.sleep("1 second"))))
  return Effect.raceFirst(operation, watch).pipe(
    Effect.onExit((exit) => exit._tag === "Failure" ? onFailure : Effect.void),
  )
}
