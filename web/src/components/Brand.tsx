/** The pgfy monogram combines an open p stem with nested storage loops. */
export function BrandMark({ size = 32 }: { size?: number }) {
  return <svg className="brand-mark" style={{ width: size, height: size }} viewBox="0 0 40 40" fill="none" aria-hidden="true">
    <path d="M7 34V17C7 9.82 12.82 4 20 4h2c7.18 0 13 5.82 13 13s-5.82 13-13 13h-5v-6h5a7 7 0 1 0 0-14h-2a7 7 0 0 0-7 7v17H7Z" fill="currentColor" />
    <path d="M17 34V17a3 3 0 0 1 3-3h2a3 3 0 1 1 0 6h-1v14h-4Z" fill="currentColor" />
  </svg>;
}

export function Brand({ onClick }: { onClick?: () => void }) {
  const content = <><BrandMark /><span className="brand-text">pgfy<span className="brand-dot">.</span></span></>;
  return onClick
    ? <button type="button" className="brand" aria-label="Pgfy home" onClick={onClick}>{content}</button>
    : <span className="brand" aria-label="Pgfy">{content}</span>;
}
