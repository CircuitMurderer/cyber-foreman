import {useEffect, useState} from "react";
import {eventTypes, type ForemanEvent} from "./api";

export type StreamState = "idle" | "connecting" | "live" | "reconnecting";

export function useTaskEvents(taskID?: string) {
  const [events, setEvents] = useState<ForemanEvent[]>([]);
  const [streamState, setStreamState] = useState<StreamState>("idle");

  useEffect(() => {
    setEvents([]);
    if (!taskID) {
      setStreamState("idle");
      return;
    }
    setStreamState("connecting");
    const source = new EventSource(`/api/v1/tasks/${encodeURIComponent(taskID)}/events`);

    const receive = (raw: Event) => {
      const message = raw as MessageEvent<string>;
      try {
        const payload = JSON.parse(message.data) as ForemanEvent | Record<string, unknown>;
        const event = "version" in payload
          ? payload as ForemanEvent
          : {
              id: message.lastEventId || `${raw.type}-${Date.now()}`,
              version: "v1",
              sequence: 0,
              task_id: taskID,
              type: raw.type,
              occurred_at: new Date().toISOString(),
              data: payload
            } satisfies ForemanEvent;
        setEvents((current) => {
          if (event.id && current.some((item) => item.id === event.id)) return current;
          const next = [...current, event];
          return next.length > 300 ? next.slice(next.length - 300) : next;
        });
      } catch {
        // Malformed external events are ignored; task polling still provides state.
      }
    };

    for (const type of eventTypes) source.addEventListener(type, receive);
    source.onopen = () => setStreamState("live");
    source.onerror = () => setStreamState("reconnecting");

    return () => {
      for (const type of eventTypes) source.removeEventListener(type, receive);
      source.close();
    };
  }, [taskID]);

  return {events, streamState};
}
