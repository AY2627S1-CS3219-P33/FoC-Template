// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend admin page and ReadinessPanel behavior.
// Author review: I validated correctness, edited for style, and added boundary checks.

import type { ServiceReadiness } from "../../api/admin";
import { getServiceReadiness } from "../../api/admin";
import { Panel } from "./metrics/Panel";
import { useMetric } from "./metrics/useMetric";

interface Props {
  /** A token for the admin-service audience with dashboard:read. */
  getToken: () => Promise<string>;
}

type Service = ServiceReadiness["services"][number];

const NAMES: Record<string, string> = {
  "order-service": "Order service",
  "supplier-service": "Supplier service",
  "credit-service": "Credit service",
  "admin-service": "Admin service",
  "user-service": "User service",
};

const REASONS: Record<NonNullable<Service["reason"]>, string> = {
  unreachable: "Cannot be reached",
  timeout: "Did not answer in time",
  not_ready: "Reports it is not ready",
  unexpected_status: "Answered with an unexpected status",
};

// A dependency a service reports as anything but working normally.
const NORMAL = new Set(["ok", "running", "closed", "disabled"]);

// Status is never colour alone: each state has its own icon and a word.
function State({ service }: { service: Service }) {
  if (service.state === "down") {
    return (
      <span className="viz-state is-down">
        <span className="viz-state-icon" aria-hidden>
          ✕
        </span>
        Down
      </span>
    );
  }
  if (service.status === "degraded") {
    return (
      <span className="viz-state is-degraded">
        <span className="viz-state-icon" aria-hidden>
          !
        </span>
        Degraded
      </span>
    );
  }
  return (
    <span className="viz-state is-up">
      <span className="viz-state-icon" aria-hidden>
        ✓
      </span>
      Up
    </span>
  );
}

// Each service's own /readyz, read live by admin-service. A service that
// reports no dependency detail shows none: nothing is made up for it.
export function ReadinessPanel({ getToken }: Props) {
  const metric = useMetric(getToken, (token) => getServiceReadiness(token), []);
  return (
    <Panel
      title="Service readiness"
      note="Each service's own /readyz, read when the page loads or you refresh."
      metric={metric}
      wide
      action={
        <button type="button" className="btn outline slim" onClick={metric.reload} disabled={metric.loading}>
          {metric.loading ? "Checking…" : "Check again"}
        </button>
      }
    >
      {(data) => (
        <>
          <ul className="viz-services">
            {data.services.map((service) => (
              <li key={service.name}>
                <span className="viz-service-name">{NAMES[service.name] ?? service.name}</span>
                <State service={service} />
                <span className="viz-service-detail muted">
                  {service.state === "down" && service.reason ? (
                    REASONS[service.reason]
                  ) : service.dependencies ? (
                    <ul className="viz-deps" aria-label={`${NAMES[service.name] ?? service.name} dependencies`}>
                      {Object.entries(service.dependencies).map(([name, status]) => (
                        <li key={name} className={NORMAL.has(status) ? "" : "is-off"}>
                          {name}: {status.replace("_", " ")}
                        </li>
                      ))}
                    </ul>
                  ) : (
                    "Reports only ready or not ready"
                  )}
                </span>
                {service.state === "up" && <span className="viz-latency muted">{service.latencyMillis} ms</span>}
              </li>
            ))}
          </ul>
          {data.notChecked.length > 0 && (
            <ul className="viz-notchecked muted">
              {data.notChecked.map((n) => (
                <li key={n.name}>
                  <strong>{NAMES[n.name] ?? n.name}</strong> is not checked: {n.reason}.
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </Panel>
  );
}
