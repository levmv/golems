/**
 * Parses a standard Server-Sent Events (SSE) stream from a fetch Response.
 * 
 * @param response The Response object from `fetch()`
 * @param onMessage Callback fired for every `data: ...` payload. 
 *                  Return `true` from the callback to stop reading the stream.
 */
export async function parseSSE(
    response: Response,
    onMessage: (data: string) => boolean | void
): Promise<void> {
    if (!response.body) throw new Error("No response body");

    const reader = response.body.getReader();
    const decoder = new TextDecoder("utf-8");
    let buffer = "";

    try {
        while (true) {
            const { done, value } = await reader.read();

            if (value) {
                buffer += decoder.decode(value, { stream: !done });
            }

            const events = buffer.split(/\r?\n\r?\n/);
            buffer = events.pop() || "";

            for (const event of events) {
                const trimmed = event.trim();
                if (!trimmed) continue;

                let data = "";
                for (const line of trimmed.split("\n").map(l => l.trimEnd())) {
                    if (line.startsWith("data:")) {
                        data += (data ? "\n" : "") + line.slice(5).trimStart();
                    }
                }

                if (data && onMessage(data)) return;
            }

            if (done) break;
        }

        // Flush any trailing event that lacked a double newline
        if (buffer.trim()) {
            let data = "";
            for (const line of buffer.split("\n").map(l => l.trimEnd())) {
                if (line.startsWith("data:")) {
                    data += (data ? "\n" : "") + line.slice(5).trimStart();
                }
            }
            if (data) onMessage(data);
        }
    } finally {
        reader.releaseLock();
    }
}