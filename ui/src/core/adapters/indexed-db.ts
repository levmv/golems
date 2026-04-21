import type { ChatSession, ChatSessionMeta, PaginatedSessions, StorageAdapter } from "../types";

export class IndexedDBAdapter implements StorageAdapter {
	private readonly DB_VERSION = 2;
	private readonly STORE_META = "session_meta";
	private readonly STORE_MSGS = "session_messages";

	private db: IDBDatabase | null = null;
	private dbPromise: Promise<IDBDatabase> | null = null;

	constructor(private dbName: string = "LLMChatDB") { }

	private async getDB(): Promise<IDBDatabase> {
		if (this.db) return this.db;
		if (this.dbPromise) return this.dbPromise;

		this.dbPromise = new Promise((resolve, reject) => {
			try {
				const request = indexedDB.open(this.dbName, this.DB_VERSION);

				request.onerror = () => {
					this.dbPromise = null;
					reject(request.error);
				};
				request.onblocked = () => {
					this.dbPromise = null;
					reject(new Error("Database upgrade blocked. Close other tabs or DevTools and refresh."));
				};
				request.onsuccess = () => {
					this.db = request.result;
					resolve(this.db);
				};

				request.onupgradeneeded = (event) => {
					const db = (event.target as IDBOpenDBRequest).result;

					if (!db.objectStoreNames.contains(this.STORE_META)) {
						const metaStore = db.createObjectStore(this.STORE_META, {
							keyPath: "id",
						});
						metaStore.createIndex("by_updated", "updatedAt", { unique: false });
					}
					if (!db.objectStoreNames.contains(this.STORE_MSGS)) {
						db.createObjectStore(this.STORE_MSGS, { keyPath: "id" });
					}
				};
			} catch (err) {
				this.dbPromise = null;
				reject(err);
			}
		});

		return this.dbPromise;
	}

	async loadSessions(limit: number, cursor?: number): Promise<PaginatedSessions> {
		const db = await this.getDB();
		return new Promise((resolve, reject) => {
			const transaction = db.transaction(this.STORE_META, "readonly");
			const store = transaction.objectStore(this.STORE_META);
			const index = store.index("by_updated");

			const sessions: ChatSessionMeta[] = [];

			// If we have a cursor (a timestamp), fetch only items older than the cursor
			const range = cursor ? IDBKeyRange.upperBound(cursor, true) : null;
			const request = index.openCursor(range, "prev");

			request.onsuccess = () => {
				const dbCursor = request.result;
				if (!dbCursor) {
					resolve({ items: sessions, hasMore: false });
					return;
				}

				sessions.push(dbCursor.value);

				// Fetch one extra item to instantly know if there are more in the DB
				if (sessions.length <= limit) {
					dbCursor.continue();
				} else {
					// We hit limit + 1. Remove the extra item, and set hasMore to true.
					sessions.pop();
					resolve({ items: sessions, hasMore: true });
				}
			};

			request.onerror = () => reject(request.error);
		});
	}

	async loadOne(id: string): Promise<ChatSession | null> {
		const db = await this.getDB();
		return new Promise((resolve, reject) => {
			const transaction = db.transaction([this.STORE_META, this.STORE_MSGS], "readonly");

			const metaReq = transaction.objectStore(this.STORE_META).get(id);
			const msgReq = transaction.objectStore(this.STORE_MSGS).get(id);

			transaction.oncomplete = () => {
				if (!metaReq.result || !msgReq.result) resolve(null);
				else resolve({ ...metaReq.result, messages: msgReq.result.messages });
			};
			transaction.onerror = () => reject(transaction.error);
		});
	}

	async updateMetadata(id: string, meta: Partial<ChatSessionMeta>): Promise<void> {
		const db = await this.getDB();
		return new Promise((resolve, reject) => {
			const transaction = db.transaction(this.STORE_META, "readwrite");
			const store = transaction.objectStore(this.STORE_META);

			const getReq = store.get(id);

			getReq.onsuccess = () => {
				const existing = getReq.result;
				if (existing) store.put({ ...existing, ...meta });
			};

			transaction.oncomplete = () => resolve();
			transaction.onerror = () => reject(transaction.error);
		});
	}

	async save(session: ChatSession): Promise<void> {
		const db = await this.getDB();
		return new Promise((resolve, reject) => {
			const transaction = db.transaction([this.STORE_META, this.STORE_MSGS], "readwrite");

			const metaStore = transaction.objectStore(this.STORE_META);
			const msgStore = transaction.objectStore(this.STORE_MSGS);

			const updatedAt = session.updatedAt || Date.now();

			metaStore.put({ id: session.id, title: session.title, updatedAt });
			msgStore.put({ id: session.id, messages: session.messages });

			transaction.oncomplete = () => resolve();
			transaction.onerror = () => reject(transaction.error);
		});
	}

	async delete(id: string): Promise<void> {
		const db = await this.getDB();
		return new Promise((resolve, reject) => {
			const transaction = db.transaction([this.STORE_META, this.STORE_MSGS], "readwrite");

			transaction.objectStore(this.STORE_META).delete(id);
			transaction.objectStore(this.STORE_MSGS).delete(id);

			transaction.oncomplete = () => resolve();
			transaction.onerror = () => reject(transaction.error);
		});
	}

	close(): void {
		if (this.db) {
			this.db.close();
			this.db = null;
			this.dbPromise = null;
		}
	}
}
