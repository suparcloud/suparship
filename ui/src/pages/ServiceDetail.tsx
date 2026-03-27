import { useParams } from "react-router-dom";

export function ServiceDetail() {
  const { project, service } = useParams<{
    project: string;
    service: string;
  }>();

  return (
    <div>
      <h1 className="text-2xl font-semibold">
        {project} / {service}
      </h1>
      <p className="mt-2 text-gray-500">Service detail placeholder.</p>
    </div>
  );
}
