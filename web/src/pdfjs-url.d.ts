// Vite's ?url import of pdf.js's worker (PdfJsPages).
declare module 'pdfjs-dist/legacy/build/pdf.worker.min.mjs?url' {
  const url: string
  export default url
}
