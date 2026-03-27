import { Link } from "react-router-dom";

export function Login() {
  return (
    <div className="flex min-h-screen items-center justify-center bg-gray-50">
      <div className="w-full max-w-sm rounded-lg border border-gray-200 bg-white p-8 shadow-sm">
        <h1 className="text-center text-2xl font-semibold">suparShip</h1>
        <p className="mt-1 text-center text-sm text-gray-500">
          Sign in to continue
        </p>
        <div className="mt-8 rounded-md bg-gray-100 p-4 text-center text-sm text-gray-500">
          Login form placeholder
        </div>
        <Link
          to="/"
          className="mt-4 block text-center text-sm text-blue-600 hover:underline"
        >
          Skip to dashboard (dev)
        </Link>
      </div>
    </div>
  );
}
