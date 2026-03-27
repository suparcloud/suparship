import { Link } from "react-router-dom";

export function Onboarding() {
  return (
    <div className="flex min-h-screen items-center justify-center bg-gray-50">
      <div className="w-full max-w-lg rounded-lg border border-gray-200 bg-white p-8 shadow-sm">
        <h1 className="text-2xl font-semibold">Welcome to suparShip</h1>
        <p className="mt-2 text-gray-500">
          Let&rsquo;s set up your first project and environment.
        </p>
        <div className="mt-8 rounded-md bg-gray-100 p-4 text-center text-sm text-gray-500">
          Onboarding wizard placeholder
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
