import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import '@fontsource/golos-text/cyrillic-400.css'
import '@fontsource/golos-text/cyrillic-500.css'
import '@fontsource/golos-text/cyrillic-600.css'
import '@fontsource/golos-text/latin-400.css'
import '@fontsource/golos-text/latin-500.css'
import '@fontsource/golos-text/latin-600.css'
import './styles.css'
import App from './App.jsx'

createRoot(document.getElementById('root')).render(<StrictMode><App /></StrictMode>)
