/** @type {import('tailwindcss').Config} */
const defaultTheme = require('tailwindcss/defaultTheme')

// Nord (nordtheme.com) with Aurora green as the accent. The stock Tailwind hues are
// remapped here, so existing utilities like text-emerald-400 pick up the palette.
const gray = { 50: '#F2F4F8', 100: '#ECEFF4', 200: '#E5E9F0', 300: '#D8DEE9', 400: '#A5AFC2', 500: '#7B88A1', 600: '#4C566A', 700: '#434C5E', 800: '#3B4252', 900: '#2E3440', 950: '#242933' }
const green = { 50: '#F1F5EC', 100: '#E2EBD8', 200: '#CEDDBF', 300: '#B9CEA6', 400: '#A3BE8C', 500: '#7E9F66', 600: '#54743D', 700: '#4C6A3A', 800: '#3E5631', 900: '#33472A', 950: '#1E2B18' }
const teal = { 50: '#EFF6F6', 100: '#DCEBEA', 200: '#C4DDDC', 300: '#AACDCB', 400: '#8FBCBB', 500: '#6C9D9C', 600: '#4F7D7C', 700: '#41666A', 800: '#365457', 900: '#2D4648', 950: '#1C2E30' }
const cyan = { 50: '#EEF7FA', 100: '#D9EEF3', 200: '#C0E2EB', 300: '#A5D3E0', 400: '#88C0D0', 500: '#62A0B2', 600: '#477F91', 700: '#3B6878', 800: '#325662', 900: '#2A4852', 950: '#1B2F37' }
const blue = { 50: '#EEF2F7', 100: '#DCE5EF', 200: '#C4D3E3', 300: '#B4C8DD', 400: '#9DB8D2', 500: '#5E81AC', 600: '#4C6C95', 700: '#3F597B', 800: '#354A65', 900: '#2C3E54', 950: '#1C2838' }
const purple = { 50: '#F6F0F5', 100: '#EBDFE9', 200: '#DCCAD9', 300: '#D6BFD2', 400: '#C8A9C2', 500: '#9A6F93', 600: '#7E5678', 700: '#684763', 800: '#563B52', 900: '#483244', 950: '#2F2029' }
const red = { 50: '#FAEFF0', 100: '#F4DEE0', 200: '#EBC5C9', 300: '#E8BCC1', 400: '#DFA0A7', 500: '#BF616A', 600: '#A8434E', 700: '#8C3741', 800: '#742F37', 900: '#61292F', 950: '#3F1A1E' }
const orange = { 50: '#FBF1ED', 100: '#F5E2DA', 200: '#ECCDBF', 300: '#E8BDAB', 400: '#DFA38E', 500: '#BD6C53', 600: '#A35640', 700: '#864637', 800: '#6E3A2E', 900: '#5B3127', 950: '#3B1F19' }
const yellow = { 50: '#FDF8EE', 100: '#FAF0DB', 200: '#F5E5C2', 300: '#F0D9A7', 400: '#EBCB8B', 500: '#CFA75B', 600: '#AB843C', 700: '#8B6A30', 800: '#715629', 900: '#5E4823', 950: '#3D2E16' }

module.exports = {
  darkMode: ["class"],
  // Add light mode variant for dark-first design
  plugins: [
    require("tailwindcss-animate"),
    function({ addVariant }) {
      addVariant('light', '.light &')
    }
  ],
  content: [
    "./index.html",
    "./src/**/*.{vue,js,ts,jsx,tsx}",
  ],
  theme: {
  	container: {
  		center: true,
  		padding: '2rem',
  		screens: {
  			'2xl': '1400px'
  		}
  	},
  	extend: {
  		fontFamily: {
  			sans: ['Inter', ...defaultTheme.fontFamily.sans],
  		},
  		colors: {
  			white: 'rgb(var(--white-rgb) / <alpha-value>)',
  			gray, zinc: gray, slate: gray, neutral: gray, stone: gray,
  			green, emerald: green, lime: green,
  			teal,
  			cyan, sky: cyan,
  			blue, indigo: blue,
  			purple, violet: purple, fuchsia: purple, pink: purple,
  			red, rose: red,
  			orange,
  			yellow, amber: yellow,
  			border: 'hsl(var(--border))',
  			input: 'hsl(var(--input))',
  			ring: 'hsl(var(--ring))',
  			background: 'hsl(var(--background))',
  			foreground: 'hsl(var(--foreground))',
  			primary: {
  				DEFAULT: 'hsl(var(--primary))',
  				foreground: 'hsl(var(--primary-foreground))'
  			},
  			secondary: {
  				DEFAULT: 'hsl(var(--secondary))',
  				foreground: 'hsl(var(--secondary-foreground))'
  			},
  			destructive: {
  				DEFAULT: 'hsl(var(--destructive))',
  				foreground: 'hsl(var(--destructive-foreground))'
  			},
  			muted: {
  				DEFAULT: 'hsl(var(--muted))',
  				foreground: 'hsl(var(--muted-foreground))'
  			},
  			accent: {
  				DEFAULT: 'hsl(var(--accent))',
  				foreground: 'hsl(var(--accent-foreground))'
  			},
  			popover: {
  				DEFAULT: 'hsl(var(--popover))',
  				foreground: 'hsl(var(--popover-foreground))'
  			},
  			card: {
  				DEFAULT: 'hsl(var(--card))',
  				foreground: 'hsl(var(--card-foreground))'
  			},
  			whatsapp: {
  				green: '#25D366',
  				teal: '#128C7E',
  				'teal-dark': '#075E54',
  				light: '#DCF8C6'
  			},
  			facebook: {
  				DEFAULT: '#1877F2',
  				dark: '#0C5DC7',
  				hover: '#166FE5',
  				hoverDark: '#0A4DAD'
  			},
  			// Violet color palette (primary accent)
  			violet: {
  				50: '#f5f3ff',
  				100: '#ede9fe',
  				200: '#ddd6fe',
  				300: '#c4b5fd',
  				400: '#a78bfa',
  				500: '#8b5cf6',
  				600: '#7c3aed',
  				700: '#6d28d9',
  				800: '#5b21b6',
  				900: '#4c1d95',
  				950: '#2e1065'
  			},
  			// Glass effect colors
  			glass: {
  				bg: 'var(--glass-bg)',
  				'bg-hover': 'var(--glass-bg-hover)',
  				border: 'var(--glass-border)'
  			}
  		},
  		borderRadius: {
  			lg: 'var(--radius)',
  			md: 'calc(var(--radius) - 2px)',
  			sm: 'calc(var(--radius) - 4px)'
  		},
  		keyframes: {
  			'accordion-down': {
  				from: {
  					height: '0'
  				},
  				to: {
  					height: 'var(--reka-accordion-content-height)'
  				}
  			},
  			'accordion-up': {
  				from: {
  					height: 'var(--reka-accordion-content-height)'
  				},
  				to: {
  					height: '0'
  				}
  			}
  		},
  		animation: {
  			'accordion-down': 'accordion-down 0.2s ease-out',
  			'accordion-up': 'accordion-up 0.2s ease-out'
  		},
  		// Glass effect backdrop blur
  		backdropBlur: {
  			xs: '2px'
  		}
  	}
  },
}
