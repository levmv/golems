/**
 * Finds an element inside the container and throws a clear error if it is not present.
 * This ensures the Fail-Fast principle.
 */
export function queryOrThrow<T extends HTMLElement>(context: HTMLElement, selector: string): T {
	const el = context.querySelector(selector);
	if (!el) {
		throw new Error(`DOM Error: Required element "${selector}" not found inside the container.`);
	}
	return el as T;
}

export function el<K extends keyof HTMLElementTagNameMap>(
	tag: K,
	className?: string,
	props?: Partial<HTMLElementTagNameMap[K]> | null,
	children?: (HTMLElement | string | null | false | undefined)[],
): HTMLElementTagNameMap[K] {
	const element = document.createElement(tag);

	if (className) {
		element.className = className;
	}

	if (props) {
		for (const key in props) {
			// @ts-expect-error
			element[key] = props[key];
		}
	}

	if (children) {
		for (const child of children) {
			if (child) element.append(child);
		}
	}

	return element;
}

/**
 * Super lightweight DOM diffing specifically for our sanitized HTML.
 * Mutates `target` to match `source` without destroying untouched nodes.
 */
export function syncDOM(target: Node, source: Node) {
	// 1. Text nodes: update text if different
	if (target.nodeType === Node.TEXT_NODE && source.nodeType === Node.TEXT_NODE) {
		if (target.nodeValue !== source.nodeValue) {
			target.nodeValue = source.nodeValue;
		}
		return;
	}

	// 2. If node types or tags are different, replace entirely
	if (target.nodeType !== source.nodeType || (target as HTMLElement).tagName !== (source as HTMLElement).tagName) {
		target.parentNode?.replaceChild(source.cloneNode(true), target);
		return;
	}

	const elTarget = target as HTMLElement;
	const elSource = source as HTMLElement;

	// 3. Sync Attributes (Safe HTML only has a few like href, class)
	const sourceAttrs = elSource.attributes;
	const targetAttrs = elTarget.attributes;

	// Remove obsolete attributes
	for (let i = targetAttrs.length - 1; i >= 0; i--) {
		const attrName = targetAttrs[i].name;
		if (!elSource.hasAttribute(attrName)) {
			elTarget.removeAttribute(attrName);
		}
	}
	// Add/Update new attributes
	for (let i = 0; i < sourceAttrs.length; i++) {
		const attr = sourceAttrs[i];
		if (elTarget.getAttribute(attr.name) !== attr.value) {
			elTarget.setAttribute(attr.name, attr.value);
		}
	}

	const targetChildren = Array.from(target.childNodes);
	const sourceChildren = Array.from(source.childNodes);
	const max = Math.max(targetChildren.length, sourceChildren.length);

	for (let i = 0; i < max; i++) {
		if (!targetChildren[i]) {
			// New child added
			target.appendChild(sourceChildren[i].cloneNode(true));
		} else if (!sourceChildren[i]) {
			// Old child removed
			target.removeChild(targetChildren[i]);
		} else {
			// Compare existing children
			syncDOM(targetChildren[i], sourceChildren[i]);
		}
	}
}
